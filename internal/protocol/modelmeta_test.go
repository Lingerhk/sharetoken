package protocol_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"sharetoken/internal/protocol"
)

func TestModelMetaSane(t *testing.T) {
	cases := []struct {
		name string
		in   *protocol.ModelMeta
		want *protocol.ModelMeta
	}{
		{"nil", nil, nil},
		{"empty", &protocol.ModelMeta{}, nil},
		{"absurd window dropped", &protocol.ModelMeta{ContextWindow: 1 << 40, MaxOutputTokens: 8192}, &protocol.ModelMeta{MaxOutputTokens: 8192}},
		{"levels ordered, unknown and repeated dropped", &protocol.ModelMeta{ReasoningLevels: []string{"high", "turbo", "low", "high"}},
			&protocol.ModelMeta{Reasoning: true, ReasoningLevels: []string{"low", "high"}}},
		{"reasoning without levels", &protocol.ModelMeta{Reasoning: true}, &protocol.ModelMeta{Reasoning: true}},
	}
	for _, c := range cases {
		if got := c.in.Sane(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestMergeMeta(t *testing.T) {
	a := &protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 64000, ReasoningLevels: []string{"low", "medium", "high"}, Reasoning: true}
	b := &protocol.ModelMeta{ContextWindow: 128000, ReasoningLevels: []string{"medium", "high", "max"}, Reasoning: true}
	cases := []struct {
		name string
		a, b *protocol.ModelMeta
		want *protocol.ModelMeta
	}{
		{"neither", nil, nil, nil},
		{"one", a, nil, a},
		{"least window, known output, common levels", a, b,
			&protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 64000, Reasoning: true, ReasoningLevels: []string{"medium", "high"}}},
		{"levels of the one that names them", &protocol.ModelMeta{Reasoning: true}, b, b},
	}
	for _, c := range cases {
		got := protocol.MergeMeta(c.a, c.b)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got != nil && (got == c.a || got == c.b) {
			t.Errorf("%s: the merge shares its input", c.name)
		}
	}
}

// Metadata is advisory and not signed: an offer verifies with it, and still
// does when a peer of an older release drops it.
func TestOfferMetaIsNotSigned(t *testing.T) {
	now := time.Now()
	k := mustKeys(t)
	o := signedOffer(k, now)
	o.Models[0].Meta = &protocol.ModelMeta{ContextWindow: 200000}
	if err := o.Verify(now, 5*time.Minute); err != nil {
		t.Fatalf("an offer with metadata must verify: %v", err)
	}
	raw, _ := json.Marshal(o)
	var old struct {
		protocol.Offer
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.Models[0].Meta = nil
	if err := old.Verify(now, 5*time.Minute); err != nil {
		t.Fatalf("an offer whose metadata was dropped must verify: %v", err)
	}
	var plain map[string]any
	_ = json.Unmarshal(raw, &plain)
	m := plain["models"].([]any)[0].(map[string]any)
	if meta, _ := m["meta"].(map[string]any); meta["context_window"] != float64(200000) {
		t.Fatalf("meta on the wire: %v", m)
	}
	o.Models[0].Meta = nil
	raw, _ = json.Marshal(o.Models[0])
	if string(raw) != `{"model":"m","price_in":10,"price_out":20,"price_cache_read":0,"price_cache_write":0,"apis":null}` {
		t.Fatalf("a model without metadata must encode as before: %s", raw)
	}
}
