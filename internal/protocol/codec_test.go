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
