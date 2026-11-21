package coordinator

import (
	"reflect"
	"testing"

	"sharetoken/internal/protocol"
)

// The model list carries what the nodes say of a model, merged so that it
// holds for any of them, and only what a model could have.
func TestModelsCarryMeta(t *testing.T) {
	f := newFixture(t, Config{})
	withMeta := func(meta *protocol.ModelMeta) {
		k := mustKeys(t)
		o := f.signedOffer(k, "m", 10, 20, 1)
		o.Models[0].Meta = meta
		if err := f.eng.UpsertOffer(o); err != nil {
			t.Fatal(err)
		}
	}
	withMeta(&protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 1 << 40, ReasoningLevels: []string{"low", "high", "warp"}})
	withMeta(&protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 64000, ReasoningLevels: []string{"high", "low"}})
	withMeta(nil) // a node of an older release
	f.offer(mustKeys(t), "plain", 10, 20, 1)
	ms := f.eng.Models()
	if len(ms) != 2 {
		t.Fatalf("models: %+v", ms)
	}
	want := &protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 64000, Reasoning: true, ReasoningLevels: []string{"low", "high"}}
	if ms[0].Model != "m" || !reflect.DeepEqual(ms[0].Meta, want) {
		t.Errorf("m: %+v, want meta %+v", ms[0].Meta, want)
	}
	if ms[1].Model != "plain" || ms[1].Meta != nil {
		t.Errorf("a model no node describes has no metadata: %+v", ms[1])
	}
}
