package protocol_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"sharetoken/protocol"
)

// A peer that announces a large frame and then stops sending must not make
// the receiver allocate the announced size: memory grows with what arrives.
func TestRecvAllocatesOnlyWhatArrives(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], protocol.MaxFrameSize)
	frame := append(hdr[:], bytes.Repeat([]byte{' '}, 10)...)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := protocol.Recv(bytes.NewReader(frame))
	runtime.ReadMemStats(&after)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame: got %v, want unexpected EOF", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Fatalf("receiving 10 bytes of an announced 8 MiB frame allocated %d bytes", got)
	}
}

func TestRecvLimitRefusesFramesAboveTheHandlersLimit(t *testing.T) {
	var buf bytes.Buffer
	big := protocol.Chunk{Seq: 1, Sealed: bytes.Repeat([]byte{1}, protocol.MaxSmallFrame)}
	if err := protocol.Send(&buf, protocol.TypeChunk, big); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.RecvLimit(bytes.NewReader(buf.Bytes()), protocol.MaxSmallFrame); err == nil {
		t.Fatal("a frame above the handler's limit was accepted")
	}
	if env, err := protocol.Recv(bytes.NewReader(buf.Bytes())); err != nil || env.Type != protocol.TypeChunk {
		t.Fatalf("the same frame under the default limit: %v", err)
	}

	buf.Reset()
	if err := protocol.Send(&buf, protocol.TypeAck, protocol.Ack{Sig: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	if env, err := protocol.RecvLimit(&buf, protocol.MaxSmallFrame); err != nil || env.Type != protocol.TypeAck {
		t.Fatalf("small frame under the small limit: %v", err)
	}
}

// Frames larger than one read chunk arrive whole.
func TestRecvReassemblesLargeFrames(t *testing.T) {
	var buf bytes.Buffer
	want := bytes.Repeat([]byte{7}, 3<<20)
	if err := protocol.Send(&buf, protocol.TypeChunk, protocol.Chunk{Seq: 9, Sealed: want}); err != nil {
		t.Fatal(err)
	}
	env, err := protocol.Recv(iotest(&buf))
	if err != nil {
		t.Fatal(err)
	}
	var c protocol.Chunk
	if err := env.Decode(protocol.TypeChunk, &c); err != nil || c.Seq != 9 || !bytes.Equal(c.Sealed, want) {
		t.Fatalf("decode: %v seq=%d len=%d", err, c.Seq, len(c.Sealed))
	}
}

// iotest hands out at most 1000 bytes per Read, like a slow network.
func iotest(r io.Reader) io.Reader { return &dribble{r: r} }

type dribble struct{ r io.Reader }

func (d *dribble) Read(p []byte) (int, error) {
	if len(p) > 1000 {
		p = p[:1000]
	}
	return d.r.Read(p)
}
