package audit

import (
	"testing"

	"sharetoken/buyer"
	"sharetoken/coordinator"
	"sharetoken/protocol"
	"sharetoken/receipt"
)

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b     string
		min, max float64
	}{
		{"2, 3, 5, 7", "2, 3, 5, 7", 1, 1},
		{"The capital of France is Paris.", "the capital of france is paris .", 1, 1}, // case and spacing do not matter
		{"Paris is the capital of France, a country in Europe.", "Paris is the capital of France, a nation in western Europe.", 0.8, 0.95},
		{"Paris is the capital of France.", "I cannot help with that request, sorry.", 0, 0.3},
		{"巴黎是法国的首都。", "巴黎是法国的首都。", 1, 1},
		{"巴黎是法国的首都。", "柏林是德国的首都。", 0.5, 0.8},
		{"", "", 1, 1},
	}
	for _, c := range cases {
		if s := Similarity(c.a, c.b); s < c.min || s > c.max {
			t.Errorf("Similarity(%q, %q) = %.2f, want [%.2f, %.2f]", c.a, c.b, s, c.min, c.max)
		}
	}
}

func TestJudge(t *testing.T) {
	a := &Auditor{cfg: Config{MatchAt: 0.8, MismatchBelow: 0.4}}
	res := func(pt, ct uint64, text string) *buyer.Result {
		return &buyer.Result{Text: text, Receipt: receipt.SignedReceipt{Receipt: receipt.Receipt{PromptTokens: pt, CompletionTokens: ct}}}
	}
	ref := &protocol.Usage{PromptTokens: 30, CompletionTokens: 10}
	cases := []struct {
		name string
		sim  float64
		r    *buyer.Result
		want string
	}{
		{"honest", 1, res(30, 10, "2, 3, 5"), coordinator.AuditMatch},
		{"other model", 0.1, res(30, 10, "something else"), coordinator.AuditMismatch},
		{"drift", 0.6, res(30, 10, "2, 3, 7"), coordinator.AuditInconclusive},
		{"inflated prompt", 1, res(300, 10, "2, 3, 5"), coordinator.AuditMismatch},
		{"inflated completion", 1, res(30, 100, "2, 3, 5"), coordinator.AuditMismatch},
		{"slack for template differences", 1, res(40, 20, "2, 3, 5"), coordinator.AuditMatch},
	}
	for _, c := range cases {
		if got, detail := a.judge(c.sim, c.r, "2, 3, 5", ref); got != c.want {
			t.Errorf("%s: verdict %s (%s), want %s", c.name, got, detail, c.want)
		}
	}
}

func TestChallengesVary(t *testing.T) {
	a := &Auditor{}
	seen := map[string]bool{}
	for range 50 {
		seen[a.challenge()] = true
	}
	if len(seen) < 30 {
		t.Fatalf("only %d distinct challenges in 50 draws: nodes could cache answers", len(seen))
	}
}
