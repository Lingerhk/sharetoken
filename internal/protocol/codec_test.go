package protocol_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"sharetoken/internal/protocol"
)

func chunkJSON(i int, word string) []byte {
	return []byte(fmt.Sprintf(`{"id":"chatcmpl-8f3a9c2e7b1d4a6f9e0c3b2a","object":"chat.completion.chunk","created":1731600000,"model":"mock-7b","system_fingerprint":"fp_44709d6fcb","choices":[{"index":0,"delta":{"content":%q},"logprobs":null,"finish_reason":null}]}`, word))
}

func TestResponseCodecRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	enc, dec := protocol.NewResponseEncoder(), protocol.NewResponseDecoder()
	var want, got [][]byte
	frames, wire := 0, 0
	for i := 0; i < 3000; i++ {
		p := chunkJSON(i, " "+strings.Repeat("x", rng.IntN(12)))
		if i%997 == 5 {
			p = bytes.Repeat([]byte{byte(rng.IntN(256))}, 70_000) // larger than the window
		}
		want = append(want, p)
		if err := enc.Write(p); err != nil {
			t.Fatal(err)
		}
		if rng.IntN(3) == 0 || i == 2999 {
			plain, err := enc.Frame(i == 2999)
			if err != nil {
				t.Fatal(err)
			}
			frames++
			wire += len(plain)
			if len(plain) != protocol.PadFrame(len(plain)) {
				t.Fatalf("frame of %d bytes is not padded", len(plain))
			}
			recs, err := dec.Frame(plain)
			if err != nil {
				t.Fatalf("frame %d: %v", frames, err)
			}
			for _, r := range recs {
				got = append(got, bytes.Clone(r))
			}
		}
	}
	if !dec.Done() {
		t.Fatal("the final frame did not end the stream")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("record %d differs", i)
		}
	}
}

func TestResponseFramesAreSmall(t *testing.T) {
	enc, dec := protocol.NewResponseEncoder(), protocol.NewResponseDecoder()
	words := strings.Fields("the quick brown fox jumps over the lazy dog while a relay forwards ciphertexts it cannot read")
	var sizes []int
	for i := 0; i < 200; i++ {
		if err := enc.Write(chunkJSON(i, " "+words[i%len(words)])); err != nil {
			t.Fatal(err)
		}
		plain, err := enc.Frame(false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Frame(plain); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(plain))
	}
	t.Logf("first frames %v, later %v", sizes[:3], sizes[100:110])
	for _, s := range sizes[10:] {
		if s > 64 {
			t.Fatalf("a one-token frame takes %d bytes", s)
		}
	}
}

func TestRequestCodec(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("hello world ", 5000) + `"}]}`)
	plain, err := protocol.EncodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != protocol.PadRequest(len(plain)) || len(plain) > len(body)/5 {
		t.Fatalf("encoded %d bytes into %d", len(body), len(plain))
	}
	back, err := protocol.DecodeRequest(plain)
	if err != nil || !bytes.Equal(back, body) {
		t.Fatalf("round trip: %v", err)
	}
	bad := bytes.Clone(plain)
	bad[len(bad)-1] = 1
	if _, err := protocol.DecodeRequest(bad); err == nil {
		t.Fatal("non-zero padding accepted")
	}
}

func TestPadBuckets(t *testing.T) {
	for _, n := range []int{0, 1, 31, 32, 33, 1000, 1024, 1025, 5000} {
		if p := protocol.PadFrame(n); p < n || p%32 != 0 {
			t.Fatalf("PadFrame(%d) = %d", n, p)
		}
	}
	prev := 0
	for _, n := range []int{1, 1024, 1025, 2000, 100_000, 10_000_000} {
		p := protocol.PadRequest(n)
		if p < n || p < prev || (n > 1024 && p > n+n/4+64) {
			t.Fatalf("PadRequest(%d) = %d", n, p)
		}
		prev = p
	}
}

// A sealed request larger than a frame travels in pieces, and the reader
// refuses requests that are too long or longer than announced.
func TestSealedRequestFrames(t *testing.T) {
	sealed := make([]byte, 3*protocol.RequestFrameSize+123)
	for i := range sealed {
		sealed[i] = byte(i * 7)
	}
	var buf bytes.Buffer
	if err := protocol.SendSealedRequest(&buf, sealed); err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(buf.Bytes())
	read := func(max int) (*protocol.Frame, error) { return protocol.ReadFrame(r, max) }
	frames := 0
	got, err := protocol.ReadSealedRequest(read, uint64(len(sealed)), func() { frames++ })
	if err != nil || !bytes.Equal(got, sealed) || frames != 4 {
		t.Fatalf("round trip: %v, %d frames", err, frames)
	}
	if _, err := protocol.ReadSealedRequest(read, protocol.MaxSealedRequest+1, nil); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Fatalf("oversized announcement: %v", err)
	}
	r = bytes.NewReader(buf.Bytes())
	if _, err := protocol.ReadSealedRequest(read, uint64(len(sealed)-1), nil); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Fatalf("longer than announced: %v", err)
	}
}

// A frame of a few kilobytes cannot make its reader hold megabytes of zeros
// or millions of empty records, and a response has a size in all.
func TestResponseDecoderBoundsWhatItInflates(t *testing.T) {
	frame := func(enc *protocol.ResponseEncoder, records int, size int) []byte {
		t.Helper()
		rec := make([]byte, size)
		for i := 0; i < records; i++ {
			if err := enc.Write(rec); err != nil {
				t.Fatal(err)
			}
		}
		f, err := enc.Frame(false)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	enc := protocol.NewResponseEncoder()
	empty := frame(enc, 600_000, 0)
	if len(empty) > 64<<10 {
		t.Fatalf("the test frame is %d bytes", len(empty))
	}
	if _, err := protocol.NewResponseDecoder().Frame(empty); err == nil || !strings.Contains(err.Error(), "too many records") {
		t.Fatalf("600000 empty records in a frame: %v", err)
	}
	// As many small records as fit a frame are fine.
	enc = protocol.NewResponseEncoder()
	if recs, err := protocol.NewResponseDecoder().Frame(frame(enc, 100_000, 30)); err != nil || len(recs) != 100_000 {
		t.Fatalf("100000 records of 30 bytes: %d, %v", len(recs), err)
	}
	enc = protocol.NewResponseEncoder()
	if _, err := protocol.NewResponseDecoder().Frame(frame(enc, 1, protocol.MaxFrameSize+1)); err == nil || !strings.Contains(err.Error(), "decompresses to more") {
		t.Fatalf("a frame past the limit: %v", err)
	}
	enc, dec := protocol.NewResponseEncoder(), protocol.NewResponseDecoder()
	n := 0
	for ; n < 64; n++ {
		if _, err := dec.Frame(frame(enc, 1, protocol.MaxFrameSize-16)); err != nil {
			if !strings.Contains(err.Error(), "the response is larger") {
				t.Fatal(err)
			}
			break
		}
	}
	// 32 frames fill the allowance but for the charge of their records.
	if want := protocol.MaxResponseBytes/protocol.MaxFrameSize - 1; n != want {
		t.Fatalf("the response was cut after %d full frames, want %d", n, want)
	}
}
