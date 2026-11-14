package node

import (
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"time"

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
	// hides the length and timing of single tokens from the relay.
	FrameInterval = 20 * time.Millisecond
	// maxPending bounds the uncompressed bytes waiting for a frame: a buyer
	// that reads slowly slows the model server down instead of filling memory.
	maxPending = 1 << 20
)

// respWriter sends one job's response: payloads go into the compressed
// response stream (protocol.ResponseEncoder), and one goroutine cuts it into
// sealed frames, the only writer on the stream until Close returns.
type respWriter struct {
	w      io.Writer
	t      *protocol.Ticket
	key    *box.Stream
	hasher *receipt.StreamHasher

	mu       sync.Mutex
	cond     *sync.Cond
	enc      *protocol.ResponseEncoder
	pending  int             // uncompressed bytes not yet in a frame
	pendText strings.Builder // billable text of those payloads
	text     strings.Builder // billable text of the payloads delivered
	usage    *protocol.Usage
	closing  bool
	final    bool // end the compressed stream with the last frame
	err      error

	wake      chan struct{}
	done      chan struct{}
	heartbeat time.Duration

	frames    uint64 // data frames written; the next frame's sequence number
	records   int    // payloads delivered
	wireBytes int64
	beats     int
}

func newRespWriter(w io.Writer, t *protocol.Ticket, key *box.Stream, heartbeat time.Duration) *respWriter {
	rw := &respWriter{w: w, t: t, key: key, hasher: receipt.NewStreamHasher(), enc: protocol.NewResponseEncoder(),
		wake: make(chan struct{}, 1), done: make(chan struct{}), heartbeat: heartbeat}
	rw.cond = sync.NewCond(&rw.mu)
	go rw.loop()
	return rw
}

// Write adds one upstream payload. It fails once the buyer is gone.
func (rw *respWriter) Write(payload []byte) error {
	info, perr := protocol.ParseChunk(payload)
	rw.mu.Lock()
	for rw.pending > maxPending && rw.err == nil {
		rw.cond.Wait()
	}
	if rw.err != nil {
		rw.mu.Unlock()
		return rw.err
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
	select {
	case rw.wake <- struct{}{}:
	default:
	}
	return nil
}

// Close sends what is pending and stops the writer. With final the last
// frame also ends the compressed stream (a complete response); without it
// (the model server failed) the buyer gets what was produced before an error.
func (rw *respWriter) Close(final bool) error {
	rw.mu.Lock()
	rw.closing, rw.final = true, final
	rw.mu.Unlock()
	select {
	case rw.wake <- struct{}{}:
	default:
	}
	<-rw.done
	return rw.err
}

func (rw *respWriter) loop() {
	defer close(rw.done)
	beat := time.NewTimer(rw.heartbeat)
	defer beat.Stop()
	var last time.Time
	for {
		select {
		case <-rw.wake:
		case <-beat.C:
			if err := protocol.SendBinary(rw.w, protocol.BinProgress); err != nil {
				rw.fail()
				return
			}
			rw.wireBytes += 5
			rw.beats++
			beat.Reset(rw.heartbeat)
			continue
		}
		rw.mu.Lock()
		closing := rw.closing
		rw.mu.Unlock()
		if wait := FrameInterval - time.Since(last); !closing && wait > 0 {
			time.Sleep(wait)
		}
		rw.mu.Lock()
		closing, final := rw.closing, rw.final
		if rw.enc.Pending() == 0 && !(closing && final) {
			rw.mu.Unlock()
			if closing {
				return
			}
			continue
		}
		plain, err := rw.enc.Frame(closing && final)
		text, n := rw.pendText.String(), rw.enc.Records()
		rw.pending = 0
		rw.pendText.Reset()
		rw.mu.Unlock()
		if err != nil {
			rw.fail()
			return
		}
		seq := rw.frames
		sealed := rw.key.Seal(seq, plain, protocol.ChunkAAD(rw.t, seq))
		if err := protocol.SendData(rw.w, seq, sealed); err != nil {
			rw.fail()
			return
		}
		// Only what reached the buyer's stream counts.
		rw.hasher.WriteChunk(sealed)
		rw.wireBytes += int64(4 + 1 + len(binary.AppendUvarint(nil, seq)) + len(sealed))
		rw.frames++
		last = time.Now()
		beat.Reset(rw.heartbeat)
		rw.mu.Lock()
		rw.records = n
		rw.text.WriteString(text)
		rw.cond.Broadcast()
		rw.mu.Unlock()
		if closing {
			return
		}
	}
}

func (rw *respWriter) fail() {
	rw.mu.Lock()
	rw.err = errBuyerGone
	rw.cond.Broadcast()
	rw.mu.Unlock()
}

// Delivered reports the payloads and frames that reached the buyer's stream,
// and the billable text among them.
func (rw *respWriter) Delivered() (records int, frames uint64, text string) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.records, rw.frames, rw.text.String()
}

// written reports how many payloads the model server produced.
func (rw *respWriter) written() int {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.enc.Records()
}
