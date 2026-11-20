//go:build live

// Live check of the verifiers against the real reference APIs, with real
// substitute models. It spends API credit (little: short, cached requests).
//
//	DEEPSEEK_API_KEY=... OPENAI_API_KEY=... scripts/test/test.sh -tags live -run Live -v -timeout 60m ./internal/audit
package audit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/internal/coordinator"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
)

type liveSeller struct {
	name string
	api  *chatAPI
}

func liveEnv(t *testing.T) (ds, oa string) {
	ds, oa = os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("OPENAI_API_KEY")
	if ds == "" || oa == "" {
		t.Skip("needs DEEPSEEK_API_KEY and OPENAI_API_KEY")
	}
	return ds, oa
}

func liveEngine(t *testing.T) *coordinator.Engine {
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	k, _ := p2p.NewKeys()
	eng, err := coordinator.NewEngine(k.ID(), k.Signer, coordinator.Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	return eng
}

// liveRun calibrates the reference, then puts the same questions to each
// seller and judges groups of 5, as audits do.
func liveRun(t *testing.T, ref Reference, sellers []liveSeller, honest []string, n int) {
	eng := liveEngine(t)
	k, _ := p2p.NewKeys()
	a, err := New(eng, Config{References: []Reference{ref}, Coordinator: p2pInfo(k)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cal, err := a.Calibrate(ctx, ref.Model, CalibrateOptions{Samples: 30, Parallel: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("calibration %s (%s): threshold %.3f; %s", ref.Model, cal.Method, cal.Threshold, cal.Detail)

	qs := make([]Question, n)
	for i := range qs {
		qs[i] = Question{Messages: a.challenge().messages(), MaxTokens: a.cfg.MaxTokens}
	}
	type row struct {
		name     string
		scores   []*Score
		verdicts map[string]int
	}
	rows := make([]*row, len(sellers))
	var wg sync.WaitGroup
	for si, s := range sellers {
		r := &row{name: s.name, scores: make([]*Score, n), verdicts: map[string]int{}}
		rows[si] = r
		sem := make(chan struct{}, 5)
		for i, q := range qs {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				text, stopped, err := generateText(ctx, s.api, Question{Messages: q.Messages, MaxTokens: q.MaxTokens})
				if err != nil || strings.TrimSpace(text) == "" {
					t.Errorf("%s answer: %q %v", s.name, text, err)
					return
				}
				sc, err := ref.Verifier.Score(ctx, q, text, stopped, a.cfg.ScoreTokens)
				if err != nil {
					t.Errorf("%s score: %v", s.name, err)
					return
				}
				r.scores[i] = sc
			})
		}
	}
	wg.Wait()
	t.Logf("%-28s %s", "seller", "per-question score, then verdicts of groups of 5")
	for _, r := range rows {
		var cells []string
		for g := 0; g+5 <= n; g += 5 {
			var rs []questionResult
			for _, sc := range r.scores[g : g+5] {
				if sc != nil {
					rs = append(rs, questionResult{Score: sc})
				}
			}
			v, score, _ := a.judge(ref.Verifier.Method(), rs, cal.Threshold)
			r.verdicts[v]++
			cells = append(cells, fmt.Sprintf("%s(%.2f)", v[:2], score))
		}
		var per []string
		for _, sc := range r.scores {
			switch {
			case sc == nil:
				per = append(per, "err")
			case sc.Method == MethodTF:
				per = append(per, fmt.Sprintf("%.2f", sc.Top1))
			default:
				per = append(per, map[bool]string{true: "F", false: "."}[sc.Fail])
			}
		}
		t.Logf("%-28s %s | %s", r.name, strings.Join(per, " "), strings.Join(cells, " "))
		isHonest := false
		for _, h := range honest {
			isHonest = isHonest || h == r.name
		}
		if isHonest && r.verdicts[coordinator.AuditMismatch] > 0 {
			t.Errorf("honest seller %s judged a mismatch", r.name)
		}
		if !isHonest && r.verdicts[coordinator.AuditMatch] > 0 {
			t.Errorf("substitute %s judged a match", r.name)
		}
	}
}

func TestLiveDeepSeekTeacherForcing(t *testing.T) {
	dsKey, oaKey := liveEnv(t)
	params := map[string]any{"reasoning_effort": "none"} // deepseek-flash thinks by default
	v, err := NewVerifier(VerifyDeepSeekPrefix, "https://api.deepseek.com/v1", dsKey, "deepseek-flash", params)
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{Model: "deepseek-flash", UpstreamModel: "deepseek-flash", Upstream: &node.OpenAICompat{}, Verifier: v, Params: params}
	ds := func(m string, p map[string]any) *chatAPI {
		return &chatAPI{base: "https://api.deepseek.com/v1", key: dsKey, model: m, params: p}
	}
	oa := func(m string) *chatAPI { return &chatAPI{base: "https://api.openai.com/v1", key: oaKey, model: m} }
	liveRun(t, ref, []liveSeller{
		{"honest deepseek-flash", ds("deepseek-flash", params)},
		{"honest deepseek-chat (alias)", ds("deepseek-chat", nil)},
		{"deepseek-v4-pro (same family)", ds("deepseek-v4-pro", params)},
		{"gpt-4o-mini", oa("gpt-4o-mini")},
		{"gpt-4.1-mini", oa("gpt-4.1-mini")},
		{"gpt-4.1", oa("gpt-4.1")},
	}, []string{"honest deepseek-flash", "honest deepseek-chat (alias)"}, 20)
}

func TestLiveOpenAIDivergence(t *testing.T) {
	dsKey, oaKey := liveEnv(t)
	v, err := NewVerifier(VerifyOpenAIDiv, "https://api.openai.com/v1", oaKey, "gpt-4o-mini", nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{Model: "gpt-4o-mini", UpstreamModel: "gpt-4o-mini", Upstream: &node.OpenAICompat{}, Verifier: v}
	oa := func(m string) *chatAPI { return &chatAPI{base: "https://api.openai.com/v1", key: oaKey, model: m} }
	liveRun(t, ref, []liveSeller{
		{"honest gpt-4o-mini", oa("gpt-4o-mini")},
		{"gpt-4.1-mini", oa("gpt-4.1-mini")},
		{"gpt-4o", oa("gpt-4o")},
		{"deepseek-flash", &chatAPI{base: "https://api.deepseek.com/v1", key: dsKey, model: "deepseek-flash", params: map[string]any{"reasoning_effort": "none"}}},
	}, []string{"honest gpt-4o-mini"}, 20)
}

func p2pInfo(k *p2p.Keys) peer.AddrInfo { return peer.AddrInfo{ID: k.PeerID} }
