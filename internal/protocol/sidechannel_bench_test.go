//go:build bench

package protocol_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"sharetoken/internal/box"
	"sharetoken/internal/protocol"
)

// v1Frame is the size of a response chunk on the stream before this change:
// a JSON chunk frame holding the payload sealed under a fresh key, base64.
func v1Frame(payload []byte) int {
	sealed := make([]byte, len(payload)+box.Overhead)
	body, _ := json.Marshal(map[string]any{"seq": 1234, "sealed": base64.StdEncoding.EncodeToString(sealed)})
	env, _ := json.Marshal(map[string]any{"type": "chunk", "body": json.RawMessage(body)})
	return 4 + len(env)
}

// A8: can a relay read a token's length off the frame size? One token per
// frame (a slow model: no merging), after 50 tokens of warm-up.
func TestSideChannelTokenLength(t *testing.T) {
	enc := protocol.NewResponseEncoder()
	for i := 0; i < 50; i++ {
		_ = enc.Write(chunkJSON(i, " warm"))
		_, _ = enc.Frame(false)
	}
	v1 := map[int][]int{}
	v2 := map[int][]int{}
	for l := 1; l <= 16; l++ {
		for k := 0; k < 20; k++ {
			word := " " + strings.Repeat(string(rune('a'+(k+l)%26)), l-1)
			p := chunkJSON(1000+l*20+k, word)
			v1[v1Frame(p)] = appendUniq(v1[v1Frame(p)], l)
			_ = enc.Write(p)
			plain, _ := enc.Frame(false)
			n := 4 + 1 + 2 + len(plain) + box.StreamOverhead
			v2[n] = appendUniq(v2[n], l)
		}
	}
	t.Logf("A8 before: %d distinct frame sizes for 16 token lengths: %s", len(v1), show(v1))
	t.Logf("A8 after:  %d distinct frame sizes for 16 token lengths: %s", len(v2), show(v2))
}

func appendUniq(s []int, v int) []int {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func show(m map[int][]int) string {
	var b strings.Builder
	for size := 0; size < 2000; size++ {
		if ls, ok := m[size]; ok {
			fmt.Fprintf(&b, "%dB<-len%v ", size, ls)
		}
	}
	return b.String()
}

// A9: the cost of sending one one-token chunk, before and after.
func BenchmarkChunkV1(b *testing.B) {
	to, _ := box.GenerateIdentity()
	t := &protocol.Ticket{JobID: "j", Model: "m"}
	p := chunkJSON(1, " hello")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sealed, _ := box.Seal(to.Public(), p, protocol.ChunkAAD(t, uint64(i)))
		body, _ := json.Marshal(map[string]any{"seq": i, "sealed": sealed})
		_ = protocol.Send(io.Discard, "chunk", json.RawMessage(body))
	}
}

func BenchmarkChunkV2(b *testing.B) {
	node, _ := box.GenerateIdentity()
	buyer, _ := box.GenerateIdentity()
	t := &protocol.Ticket{JobID: "j", Model: "m", NodeBoxPub: node.Public()}
	key, _ := node.NewStream(buyer.Public(), protocol.ResponseKeyInfo(t, buyer.Public()))
	enc := protocol.NewResponseEncoder()
	p := chunkJSON(1, " hello")
	var out bytes.Buffer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = enc.Write(p)
		plain, _ := enc.Frame(false)
		sealed := key.Seal(uint64(i), plain, protocol.ChunkAAD(t, uint64(i)))
		out.Reset()
		_ = protocol.SendData(&out, uint64(i), sealed)
	}
}
