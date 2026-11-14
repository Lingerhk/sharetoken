package node

import (
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"sharetoken/internal/box"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
)

const (
	// HeartbeatInterval is the default Config.Heartbeat: how long the node
	// lets a job's stream stay silent before it sends a progress frame. A
	// reasoning model may say nothing for minutes, and the buyer must not take
	// that for a dead node.
	HeartbeatInterval = 10 * time.Second
	// FrameInterval is the shortest time between two response frames. The
	// first frame goes out at once; payloads that arrive within this time of
	// the previous frame wait and share the next one, which saves bytes and
	// hides the length and timing of single tokens from the relay. A relay
	// spends its CPU per frame, not per byte (test/relayload): 50 ms (20
	// frames a second, a token stream still looks fluid) cut a relay's CPU by
	// 40% against 20 ms.
	FrameInterval = 50 * time.Millisecond
	// ResumeWindow is the default Config.ResumeWindow: how long a job whose
	// buyer's stream broke keeps running and keeps its frames for the buyer
	// to come back (protocol.TypeResume) before it is billed as delivered so
	// far.
	ResumeWindow = 60 * time.Second
	// maxPending bounds the uncompressed bytes waiting for a frame: a buyer
	// that reads slowly slows the model server down instead of filling memory.
	maxPending = 1 << 20
	// maxKept bounds the sealed frames a job keeps for a resume. Frames the
	// buyer's stream took and that lie further back are dropped; a resume
	// from one of them is refused.
	maxKept = 8 << 20
)

var (
	// errBuyerGone: the buyer's stream broke and the buyer did not come back
	// within the resume window.
	errBuyerGone = errors.New("buyer stream closed")
	// errCancelled: the buyer said it stops (protocol.TypeCancel).
	errCancelled = errors.New("buyer cancelled the job")
	// errStopped: the job is over.
	errStopped = errors.New("job finished")
)

// frame is one sealed response frame, the payloads written up to it and the
// billable text of its own payloads.
type frame struct {
	sealed  []byte // nil once dropped
	records int
	text    string
}

// respWriter sends one job's response and survives the buyer's stream
// breaking. Payloads go into the compressed response stream
// (protocol.ResponseEncoder); one goroutine cuts it into sealed frames, keeps
// them, and writes them to the buyer's current stream, the only writer on it
// until the job ends. A reader goroutine per stream takes the buyer's cancel,
// ack or reject, and notices a broken stream.
//
// When the stream breaks the job keeps running: frames are still cut and
// kept, and a buyer that comes back within the resume window on a new stream
// (attach) gets every frame from the one it asks for, then the live ones.
// Each frame enters the receipt's hash once, in order, when it is first
// written, so the buyer, which hashes each frame once as it arrives, ends up
// with the same digest.
type respWriter struct {
	t         *protocol.Ticket
	key       *box.Stream
	heartbeat time.Duration
	window    time.Duration

	mu       sync.Mutex
	cond     *sync.Cond
	enc      *protocol.ResponseEncoder
	pending  int // uncompressed bytes not yet in a frame
	pendText strings.Builder
	usage    *protocol.Usage
	frames   []frame
	kept     int // bytes of frames still kept
	dropped  int // frames[:dropped] were dropped
	lastCut  time.Time
	finalCut bool

	st       network.Stream // the buyer's current stream; nil while parked
	next     int            // the next frame to write on st
	hashed   int            // frames written at least once (and hashed)
	parkedAt time.Time
	resumes  int

	closing bool
	final   bool
	trailer []byte // a frame to write after the last one (the receipt)
	trailed bool
	err     error
	beats   int
	wireOut int64
	hasher  *receipt.StreamHasher
	text    strings.Builder // billable text of the frames hashed
	records int             // payloads in the frames hashed

	ackCh    chan *protocol.Envelope
	wake     chan struct{}
	done     chan struct{}
	attachCh chan attachReq
}

type attachReq struct {
	st   network.Stream
	from uint64
	res  chan error
}

func newRespWriter(st network.Stream, t *protocol.Ticket, key *box.Stream, heartbeat, window time.Duration) *respWriter {
	rw := &respWriter{t: t, key: key, heartbeat: heartbeat, window: window, enc: protocol.NewResponseEncoder(),
		st: st, hasher: receipt.NewStreamHasher(), ackCh: make(chan *protocol.Envelope, 1),
		wake: make(chan struct{}, 1), done: make(chan struct{}), attachCh: make(chan attachReq)}
	rw.cond = sync.NewCond(&rw.mu)
	go rw.read(st)
	go rw.loop()
	return rw
}

func (rw *respWriter) kick() {
	select {
	case rw.wake <- struct{}{}:
	default:
	}
}

// Write adds one upstream payload. It fails once the buyer cancelled or is
// gone for good.
func (rw *respWriter) Write(payload []byte) error {
	info, perr := protocol.ParseChunk(payload)
	rw.mu.Lock()
	for rw.pending > maxPending && rw.err == nil {
		rw.cond.Wait()
	}
	if rw.err != nil {
		err := rw.err
		rw.mu.Unlock()
		return err
	}
	if err := rw.enc.Write(payload); err != nil {
		rw.mu.Unlock()
		return err
	}
	rw.pending += len(payload)
	if perr == nil {
		rw.pendText.WriteString(info.Text())
		if info.Usage != nil {
			rw.usage = info.Usage
		}
	}
	rw.mu.Unlock()
	rw.kick()
	return nil
}

// Close cuts the last frame and waits until every frame reached the buyer's
// stream. With final the last frame also ends the compressed stream (a
// complete response), and a broken stream is waited for until the resume
// window closes. Without it (the model server failed) a parked job gives up
// at once.
func (rw *respWriter) Close(final bool) error {
	rw.mu.Lock()
	rw.closing, rw.final = true, final
	rw.mu.Unlock()
	rw.kick()
	rw.mu.Lock()
	defer rw.mu.Unlock()
	for rw.err == nil && !(rw.cutDone() && rw.next == len(rw.frames) && rw.st != nil) {
		if !final && rw.st == nil {
			return errBuyerGone
		}
		rw.cond.Wait()
	}
	return rw.err
}

// cutDone reports whether nothing is left to cut into a frame. Under mu.
func (rw *respWriter) cutDone() bool {
	if rw.closing && rw.final {
		return rw.finalCut
	}
	return rw.enc.Pending() == 0
}

// Finish writes trailer (the receipt frame) after the last frame, with the
// same resume rules.
func (rw *respWriter) Finish(trailer []byte) error {
	rw.mu.Lock()
	rw.trailer = trailer
	rw.mu.Unlock()
	rw.kick()
	rw.mu.Lock()
	defer rw.mu.Unlock()
	for rw.err == nil && !rw.trailed {
		rw.cond.Wait()
	}
	if rw.trailed {
		return nil
	}
	return rw.err
}

// Ack waits for the buyer's verdict on the receipt.
func (rw *respWriter) Ack(timeout time.Duration) *protocol.Envelope {
	select {
	case env := <-rw.ackCh:
		return env
	case <-time.After(timeout):
		return nil
	}
}

// Stop ends the writer (the job is over) and returns the buyer's current
// stream, if any, for a last frame.
func (rw *respWriter) Stop() network.Stream {
	rw.mu.Lock()
	if rw.err == nil {
		rw.err = errStopped
	}
	rw.cond.Broadcast()
	rw.mu.Unlock()
	rw.kick()
	<-rw.done
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.st
}

// attach makes st the buyer's stream, resuming at frame from.
func (rw *respWriter) attach(st network.Stream, from uint64) error {
	req := attachReq{st: st, from: from, res: make(chan error, 1)}
	select {
	case rw.attachCh <- req:
		return <-req.res
	case <-rw.done:
		return protocol.Errorf(protocol.CodeConflict, "the job is over")
	}
}

// read takes the buyer's frames on st: cancel, ack, reject. A read error on
// the current stream means the stream broke.
func (rw *respWriter) read(st network.Stream) {
	for {
		env, err := protocol.RecvLimit(st, protocol.MaxSmallFrame)
		if err != nil {
			rw.lost(st)
			return
		}
		switch env.Type {
		case protocol.TypeCancel:
			rw.mu.Lock()
			if rw.err == nil {
				rw.err = errCancelled
			}
			rw.cond.Broadcast()
			rw.mu.Unlock()
			rw.kick()
			return
		case protocol.TypeAck, protocol.TypeReject:
			select {
			case rw.ackCh <- env:
			default:
			}
			return
		}
	}
}

// lost parks the job if st is still the buyer's stream.
func (rw *respWriter) lost(st network.Stream) {
	rw.mu.Lock()
	if rw.st == st && rw.err == nil && !rw.trailed {
		_ = st.Reset()
		rw.st, rw.parkedAt = nil, time.Now()
	}
	rw.cond.Broadcast()
	rw.mu.Unlock()
	rw.kick()
}

func (rw *respWriter) loop() {
	defer close(rw.done)
	timer := time.NewTimer(rw.heartbeat)
	defer timer.Stop()
	lastWrite := time.Now()
	for {
		select {
		case <-rw.wake:
		case <-timer.C:
		case a := <-rw.attachCh:
			a.res <- rw.doAttach(a)
		}
		rw.mu.Lock()
		if rw.err != nil {
			rw.cond.Broadcast()
			rw.mu.Unlock()
			return
		}
		now := time.Now()
		if rw.st == nil && now.Sub(rw.parkedAt) > rw.window {
			rw.err = errBuyerGone
			rw.cond.Broadcast()
			rw.mu.Unlock()
			return
		}
		// Cut a frame from what the encoder holds, at most every
		// FrameInterval (at once when the job is closing).
		wait := time.Duration(0)
		if rw.enc.Pending() > 0 || (rw.closing && rw.final && !rw.finalCut) {
			if d := FrameInterval - now.Sub(rw.lastCut); d > 0 && !rw.closing {
				wait = d
			} else if err := rw.cut(); err != nil {
				rw.err = err
				rw.cond.Broadcast()
				rw.mu.Unlock()
				return
			}
		}
		// What to write, if a stream is attached.
		st, from := rw.st, rw.next
		var batch [][]byte
		if st != nil {
			for i := from; i < len(rw.frames); i++ {
				batch = append(batch, rw.frames[i].sealed)
			}
		}
		trailer := st != nil && rw.trailer != nil && !rw.trailed && rw.cutDone()
		rw.mu.Unlock()

		ok := true
		for i, sealed := range batch {
			if err := protocol.SendData(st, uint64(from+i), sealed); err != nil {
				ok = false
				break
			}
			lastWrite = time.Now()
			rw.wrote(st, from+i)
		}
		if ok && trailer {
			if _, err := st.Write(rw.trailer); err != nil {
				ok = false
			} else {
				lastWrite = time.Now()
				rw.mu.Lock()
				rw.trailed = true
				rw.cond.Broadcast()
				rw.mu.Unlock()
			}
		}
		if ok && st != nil && len(batch) == 0 && !trailer && time.Since(lastWrite) >= rw.heartbeat {
			if err := protocol.SendBinary(st, protocol.BinProgress); err != nil {
				ok = false
			} else {
				lastWrite = time.Now()
				rw.mu.Lock()
				rw.beats++
				rw.wireOut += 5
				rw.mu.Unlock()
			}
		}
		if !ok {
			rw.lost(st)
		}

		// Sleep until the next frame is due, the heartbeat, or the end of
		// the resume window.
		rw.mu.Lock()
		sleep := rw.heartbeat - time.Since(lastWrite)
		if wait > 0 {
			sleep = min(sleep, wait)
		}
		if rw.st == nil {
			sleep = min(sleep, rw.window-time.Since(rw.parkedAt)+time.Millisecond)
		}
		rw.mu.Unlock()
		timer.Reset(max(sleep, time.Millisecond))
	}
}

// cut seals the encoder's pending records into the next frame. Under mu.
func (rw *respWriter) cut() error {
	final := rw.closing && rw.final
	plain, err := rw.enc.Frame(final)
	if err != nil {
		return err
	}
	seq := uint64(len(rw.frames))
	sealed := rw.key.Seal(seq, plain, protocol.ChunkAAD(rw.t, seq))
	rw.frames = append(rw.frames, frame{sealed: sealed, records: rw.enc.Records(), text: rw.pendText.String()})
	rw.kept += len(sealed)
	rw.pendText.Reset()
	rw.pending = 0
	rw.lastCut = time.Now()
	rw.finalCut = final
	// Drop the oldest frames the buyer's stream took once too much is kept.
	for rw.kept > maxKept && rw.dropped < rw.hashed-1 {
		rw.kept -= len(rw.frames[rw.dropped].sealed)
		rw.frames[rw.dropped].sealed = nil
		rw.dropped++
	}
	rw.cond.Broadcast()
	return nil
}

// wrote records that frame i went out on st.
func (rw *respWriter) wrote(st network.Stream, i int) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.st != st {
		return
	}
	rw.next = i + 1
	sealed := rw.frames[i].sealed
	rw.wireOut += int64(4 + 1 + len(binary.AppendUvarint(nil, uint64(i))) + len(sealed))
	if i == rw.hashed {
		// Only what reached the buyer's stream counts, once.
		rw.hasher.WriteChunk(sealed)
		rw.text.WriteString(rw.frames[i].text)
		rw.records = rw.frames[i].records
		rw.hashed++
	}
	rw.cond.Broadcast()
}

// doAttach switches to the buyer's new stream. Runs on the loop.
func (rw *respWriter) doAttach(a attachReq) error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	from := int(a.from)
	switch {
	case rw.err != nil || rw.trailed:
		return protocol.Errorf(protocol.CodeConflict, "the job is over")
	case a.from > uint64(rw.hashed):
		return protocol.Errorf(protocol.CodeBadRequest, "frame %d was never sent", a.from)
	case from < rw.dropped:
		return protocol.Errorf(protocol.CodeConflict, "frame %d is no longer kept", from)
	}
	if rw.st != nil {
		_ = rw.st.Reset()
	}
	rw.st, rw.next = a.st, from
	rw.resumes++
	go rw.read(a.st)
	rw.cond.Broadcast()
	return nil
}

// Delivered reports the payloads and frames that reached the buyer's stream,
// and the billable text among them.
func (rw *respWriter) Delivered() (records int, frames int, text string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.records, rw.hashed, rw.text.String()
}

// written reports how many payloads the model server produced.
func (rw *respWriter) written() int {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.enc.Records()
}

// Digest is the hash of the frames that reached the buyer, for the receipt.
func (rw *respWriter) Digest() []byte {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.hasher.Sum()
}
