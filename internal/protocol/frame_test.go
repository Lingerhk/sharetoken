package protocol_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"sharetoken/internal/protocol"
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
	big := protocol.Reject{Reason: strings.Repeat("x", protocol.MaxSmallFrame)}
	if err := protocol.Send(&buf, protocol.TypeReject, big); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.RecvLimit(bytes.NewReader(buf.Bytes()), protocol.MaxSmallFrame); err == nil {
		t.Fatal("a frame above the handler's limit was accepted")
	}
	if env, err := protocol.Recv(bytes.NewReader(buf.Bytes())); err != nil || env.Type != protocol.TypeReject {
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
	if err := protocol.SendData(&buf, 9, want); err != nil {
		t.Fatal(err)
	}
	f, err := protocol.ReadFrame(iotest(&buf), protocol.MaxFrameSize)
	if err != nil {
		t.Fatal(err)
	}
	seq, sealed, err := protocol.DecodeData(f.Payload)
	if err != nil || f.Env != nil || f.Kind != protocol.BinData || seq != 9 || !bytes.Equal(sealed, want) {
		t.Fatalf("decode: %v seq=%d len=%d", err, seq, len(sealed))
	}
}

// A binary frame is not a JSON one: Recv refuses it, ReadFrame returns both.
func TestBinaryAndJSONFramesShareAStream(t *testing.T) {
	var buf bytes.Buffer
	_ = protocol.SendBinary(&buf, protocol.BinProgress)
	_ = protocol.Send(&buf, protocol.TypeAck, protocol.Ack{Sig: []byte{1}})
	_ = protocol.SendBinary(&buf, protocol.BinRequest, []byte("sealed"))
	if _, err := protocol.Recv(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("Recv accepted a binary frame")
	}
	r := bytes.NewReader(buf.Bytes())
	for i, want := range []byte{protocol.BinProgress, 0, protocol.BinRequest} {
		f, err := protocol.ReadFrame(r, protocol.MaxFrameSize)
		if err != nil {
			t.Fatal(err)
		}
		if want == 0 {
			if f.Env == nil || f.Env.Type != protocol.TypeAck {
				t.Fatalf("frame %d: want the ack", i)
			}
			continue
		}
		if f.Env != nil || f.Kind != want {
			t.Fatalf("frame %d: kind %d, want %d", i, f.Kind, want)
		}
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

// A handler of several request types gives each its own size: the type is
// read from the frame's beginning before the rest is taken, and a frame that
// does not begin with its type gets the limit of type "".
func TestRecvTypedLimitsByType(t *testing.T) {
	limit := func(typ string) int {
		if typ == protocol.TypeSettle {
			return protocol.MaxFrameSize
		}
		return protocol.MaxSmallFrame
	}
	frame := func(typ string, body any) []byte {
		var buf bytes.Buffer
		if err := protocol.SendRequest(&buf, typ, body); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	big := protocol.Reject{Reason: strings.Repeat("x", 2*protocol.MaxSmallFrame)}
	if env, err := protocol.RecvTyped(bytes.NewReader(frame(protocol.TypeSettle, big)), limit); err != nil || env.Type != protocol.TypeSettle {
		t.Fatalf("a large frame of a type that may be large: %v", err)
	}
	if env, err := protocol.RecvTyped(bytes.NewReader(frame(protocol.TypeAnnounce, protocol.Reject{Reason: "x"})), limit); err != nil || env.Type != protocol.TypeAnnounce {
		t.Fatalf("a small frame: %v", err)
	}
	// Refused after the first chunk: the megabytes behind it are never read.
	r := &countingReader{r: bytes.NewReader(frame(protocol.TypeAnnounce, protocol.Reject{Reason: strings.Repeat("x", 4<<20)}))}
	if _, err := protocol.RecvTyped(r, limit); err == nil {
		t.Fatal("a large frame of a type that must be small was accepted")
	}
	if r.n > 2*protocol.MaxSmallFrame {
		t.Fatalf("%d bytes of a refused frame were read", r.n)
	}
	// The type somewhere else than first: read as no type at all.
	hidden := []byte(`{"body":{"reason":"` + strings.Repeat("x", 2*protocol.MaxSmallFrame) + `"},"type":"settle","ver":"1.0.5"}`)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(hidden)))
	if _, err := protocol.RecvTyped(bytes.NewReader(append(hdr[:], hidden...)), limit); err == nil {
		t.Fatal("a large frame that hides its type was accepted")
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
