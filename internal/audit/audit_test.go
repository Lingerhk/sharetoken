package audit

import (
	"context"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/coordinator"
	"sharetoken/internal/p2p"
)

// words splits text into word tokens with their leading space, the way a
// BPE tokenizer splits English.
func words(s string) []string {
	var out []string
	start := 0
	for i := 1; i < len(s); i++ {
		if s[i] == ' ' {
			out = append(out, s[start:i])
			start = i
		}
	}
	return append(out, s[start:])
}

// fakeModel answers every question with canon, word by word: its first
// choice after a prefix of canon is canon's next word, and after anything
// else a fixed word. alt is its second choice everywhere.
type fakeModel struct {
	canon string
	alt   string
	calls int
}

func (f *fakeModel) next(prefix string) (first string, ends bool) {
	toks := words(f.canon)
	if !strings.HasPrefix(f.canon, prefix) {
		return " banana", false
	}
	pos := 0
	for _, t := range toks {
		if pos >= len(prefix) {
			return t, false
		}
		pos += len(t)
	}
	return "", true
}

func byteInts(s string) []int {
	out := make([]int, len(s))
	for i := range len(s) {
		out[i] = int(s[i])
	}
	return out
}

// deepseekServer mimics DeepSeek's beta prefix completion with top_logprobs.
func deepseekServer(t *testing.T, f *fakeModel) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]any `json:"messages"`
			MaxTok   int              `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.calls++
		prefix := ""
		if last := req.Messages[len(req.Messages)-1]; last["role"] == "assistant" {
			if !strings.HasSuffix(r.URL.Path, "/beta/chat/completions") || last["prefix"] != true {
				t.Errorf("prefix completion must use the beta endpoint and prefix: true (%s)", r.URL.Path)
			}
			prefix = last["content"].(string)
		}
		first, ends := f.next(prefix)
		resp := map[string]any{"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 1}}
		if ends {
			resp["choices"] = []any{map[string]any{"message": map[string]any{"content": ""}, "finish_reason": "stop", "logprobs": map[string]any{"content": []any{}}}}
		} else {
			top := []any{
				map[string]any{"token": first, "bytes": byteInts(first), "logprob": 0},
				map[string]any{"token": f.alt, "bytes": byteInts(f.alt), "logprob": -9999},
			}
			resp["choices"] = []any{map[string]any{"message": map[string]any{"content": first}, "finish_reason": "length",
				"logprobs": map[string]any{"content": []any{map[string]any{"token": first, "bytes": byteInts(first), "logprob": 0, "top_logprobs": top}}}}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var q1 = Question{Messages: []Message{{Role: "user", Content: "Tell me about bread."}}, MaxTokens: 96}

func TestDeepSeekTeacherForcing(t *testing.T) {
	canon := "Bread is made from flour, water and yeast."
	f := &fakeModel{canon: canon, alt: " rice"}
	v, err := NewVerifier(VerifyDeepSeekPrefix, deepseekServer(t, f).URL+"/v1", "k", "deepseek-chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	s, err := v.Score(ctx, q1, canon, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Top1 != 1 || s.EndAgrees == nil || !*s.EndAgrees || s.Tokens != len(words(canon))+1 || s.RefPrompt != 20 {
		t.Fatalf("the reference's own answer: %+v", s)
	}

	// Another model's answer: only the shared opening is the first choice.
	s, err = v.Score(ctx, q1, "Bread is a staple food baked from dough.", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Top1 > 0.4 {
		t.Fatalf("another model's answer scored top-1 %.2f: %+v", s.Top1, s)
	}

	// A cut-off answer is not asked to end; the scored positions are capped.
	s, err = v.Score(ctx, q1, canon, false, 3)
	if err != nil || s.Tokens != 3 || s.EndAgrees != nil || s.Top1 != 1 {
		t.Fatalf("capped: %+v %v", s, err)
	}

	// Multi-byte text: prefixes sent back always end on a character boundary.
	f.canon = "面包由面粉和水做成。"
	if s, err = v.Score(ctx, q1, "面包很好吃。", true, 0); err != nil || s.Top1 >= 1 {
		t.Fatalf("CJK: %+v %v", s, err)
	}
}

func TestCharBoundary(t *testing.T) {
	b := []byte("面包")
	for n := 1; n <= 3; n++ {
		if got := charBoundary(b, n); got != 3 {
			t.Fatalf("charBoundary(%d) = %d, want 3", n, got)
		}
	}
	if got := charBoundary([]byte("ab"), 5); got != 2 {
		t.Fatal(got)
	}
}

// vllmServer mimics vLLM's /tokenize and chat completions with
// prompt_logprobs: tokens are bytes; the model's first choice at each answer
// position is canon's next byte.
func vllmServer(t *testing.T, canon string) *httptest.Server {
	const tmplHead = "<user>"
	render := func(msgs []Message, continueFinal bool) []int {
		var sb strings.Builder
		for _, m := range msgs {
			if m.Role == "assistant" && continueFinal {
				sb.WriteString("<asst>" + m.Content)
				continue
			}
			sb.WriteString(tmplHead + m.Content + "</>")
		}
		if !continueFinal {
			sb.WriteString("<asst>")
		}
		return byteInts(sb.String())
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages      []Message `json:"messages"`
			ContinueFinal bool      `json:"continue_final_message"`
			PromptLP      int       `json:"prompt_logprobs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		toks := render(req.Messages, req.ContinueFinal)
		if r.URL.Path == "/tokenize" {
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": toks, "count": len(toks)})
			return
		}
		if r.URL.Path != "/v1/chat/completions" || req.PromptLP != 20 || !req.ContinueFinal {
			t.Errorf("unexpected call %s %+v", r.URL.Path, req)
		}
		head := len(render(req.Messages[:len(req.Messages)-1], false))
		lps := make([]any, len(toks))
		for i := 1; i < len(toks); i++ {
			pos := map[string]any{}
			want := toks[i] // the prompt part: the model agrees
			if i >= head {
				if k := i - head; k < len(canon) {
					want = int(canon[k])
				} else {
					want = '#'
				}
			}
			pos[strconv.Itoa(want)] = map[string]any{"logprob": -0.1, "rank": 1}
			if toks[i] != want {
				pos[strconv.Itoa(toks[i])] = map[string]any{"logprob": -3.5, "rank": 7}
			}
			lps[i] = pos
		}
		content := ""
		if len(toks)-head < len(canon) {
			content = "x"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_logprobs": lps,
			"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": map[bool]string{true: "stop", false: "length"}[content == ""]}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVLLMTeacherForcing(t *testing.T) {
	canon := "Bread is baked."
	v, _ := NewVerifier(VerifyVLLM, vllmServer(t, canon).URL+"/v1", "", "qwen", nil)
	s, err := v.Score(context.Background(), q1, canon, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Top1 != 1 || s.BigGaps != 0 || s.RefCompletion != uint64(len(canon)) || s.EndAgrees == nil || !*s.EndAgrees {
		t.Fatalf("honest: %+v", s)
	}
	s, err = v.Score(context.Background(), q1, "Bread is fried!", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Top1 > 0.8 || s.BigGaps == 0 {
		t.Fatalf("other model: %+v", s)
	}
}

func tok(s string, lp float64, top ...topLogprob) tokenLogprob {
	return tokenLogprob{topLogprob: topLogprob{Token: s, Logprob: lp}, Top: append([]topLogprob{{Token: s, Logprob: lp}}, top...)}
}

func TestFirstDivergence(t *testing.T) {
	ref := []tokenLogprob{
		tok("Paris", -0.01), tok(" is", -0.02, topLogprob{Token: " was", Logprob: -0.5}, topLogprob{Token: " remains", Logprob: -4}),
		tok(" big", -0.1),
	}
	cases := []struct {
		answer  string
		stopped bool
		fail    bool
		kind    string
	}{
		{"Paris is big", true, false, "none"},
		{"Paris was big", true, false, "token"},          // a near tie (0.48 nats)
		{"Paris remains big", true, true, "token"},       // 3.98 nats below
		{"Paris, the capital", true, true, "token"},      // not in the top 20
		{"Paris is", true, true, "seller stopped early"}, // ended where the reference goes on
		{"Paris is", false, false, "seller truncated"},   // ran into its limit
		{"Paris is big and old", true, true, "seller went on"},
	}
	for _, c := range cases {
		s := firstDivergence(ref, "stop", []byte(c.answer), c.stopped)
		if s.Fail != c.fail || s.DivKind != c.kind {
			t.Errorf("%q: %+v", c.answer, s)
		}
	}
	if s := firstDivergence(ref, "length", []byte("Paris is big and old"), true); s.Fail {
		t.Errorf("the reference stopped at its limit, so a longer answer is fine: %+v", s)
	}
}

func TestOpenAIDivergenceVerifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["logprobs"] != true || req["top_logprobs"] != float64(20) || req["temperature"] != float64(0) {
			t.Errorf("request %v", req)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Hi there"},"finish_reason":"stop","logprobs":{"content":[
			{"token":"Hi","bytes":[72,105],"logprob":-0.1,"top_logprobs":[{"token":"Hi","bytes":[72,105],"logprob":-0.1},{"token":"Hello","bytes":[72,101,108,108,111],"logprob":-0.4}]},
			{"token":" there","bytes":[32,116,104,101,114,101],"logprob":-0.2,"top_logprobs":[{"token":" there","bytes":[32,116,104,101,114,101],"logprob":-0.2}]}]}}],
			"usage":{"prompt_tokens":12,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	v, _ := NewVerifier(VerifyOpenAIDiv, srv.URL+"/v1", "k", "gpt-4o-mini", nil)
	if s, err := v.Score(context.Background(), q1, "Hello there", true, 0); err != nil || s.Fail || s.DivRank != 2 || s.RefPrompt != 12 {
		t.Fatalf("near tie: %+v %v", s, err)
	}
	if s, err := v.Score(context.Background(), q1, "Greetings, friend", true, 0); err != nil || !s.Fail {
		t.Fatalf("another model: %+v %v", s, err)
	}
}

func TestJudge(t *testing.T) {
	a := &Auditor{cfg: Config{Threshold: 0.85, Margin: 0.03, DivFails: 2, ScoreTokens: 64}}
	tf := func(billedPrompt, billedOut uint64, top1 ...float64) []questionResult {
		var rs []questionResult
		for _, x := range top1 {
			rs = append(rs, questionResult{Billed: [2]uint64{billedPrompt, billedOut}, Score: &Score{Method: MethodTF, Top1: x, Tokens: 40, RefPrompt: 30, RefCompletion: 39}})
		}
		return rs
	}
	div := func(fails int) []questionResult {
		var rs []questionResult
		for i := range 5 {
			rs = append(rs, questionResult{Billed: [2]uint64{30, 10}, Score: &Score{Method: MethodDiv, Fail: i < fails, RefPrompt: 30, RefCompletion: 10}})
		}
		return rs
	}
	cases := []struct {
		name, method string
		rs           []questionResult
		want         string
	}{
		{"honest", MethodTF, tf(30, 39, 0.97, 0.92, 0.95, 0.88, 0.99), coordinator.AuditMatch},
		{"substitute", MethodTF, tf(30, 39, 0.70, 0.62, 0.86, 0.75, 0.66), coordinator.AuditMismatch},
		{"near the threshold", MethodTF, tf(30, 39, 0.86, 0.86, 0.86, 0.86, 0.86), coordinator.AuditInconclusive},
		{"inflated prompt", MethodTF, tf(300, 39, 0.97, 0.97), coordinator.AuditMismatch},
		{"inflated completion", MethodTF, tf(30, 400, 0.97, 0.97), coordinator.AuditMismatch},
		{"template slack", MethodTF, tf(40, 50, 0.97, 0.97), coordinator.AuditMatch},
		{"another model may bill its own tokens", MethodTF, tf(30, 400, 0.5, 0.5), coordinator.AuditMismatch},
		{"div clean", MethodDiv, div(0), coordinator.AuditMatch},
		{"div one", MethodDiv, div(1), coordinator.AuditInconclusive},
		{"div two", MethodDiv, div(2), coordinator.AuditMismatch},
		{"none", MethodNone, []questionResult{{Billed: [2]uint64{30, 10}, Score: &Score{Method: MethodNone, RefPrompt: 30}}}, coordinator.AuditInconclusive},
		{"none inflated", MethodNone, []questionResult{{Billed: [2]uint64{900, 10}, Score: &Score{Method: MethodNone, RefPrompt: 30}}}, coordinator.AuditMismatch},
	}
	for _, c := range cases {
		if got, _, detail := a.judge(c.method, c.rs, 0.85); got != c.want {
			t.Errorf("%s: verdict %s (%s), want %s", c.name, got, detail, c.want)
		}
	}
	// A partly scored answer is not held to the reference's completion count.
	rs := tf(30, 400, 0.97)
	rs[0].Score.Tokens = 64
	if got, _, d := a.judge(MethodTF, rs, 0.85); got != coordinator.AuditMatch {
		t.Errorf("capped scoring: %s %s", got, d)
	}
}

func TestOfficialCircuitsOnly(t *testing.T) {
	ids := make([]peer.ID, 3)
	for i := range ids {
		k, err := p2p.NewKeys()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = k.PeerID
	}
	official, private, node := ids[0], ids[1], ids[2]
	m := func(s string) ma.Multiaddr { return ma.StringCast(s) }
	addrs := []ma.Multiaddr{
		m("/ip4/203.0.113.7/tcp/4001"),
		m("/ip6/2001:db8::1/udp/4001/quic-v1"),
		m("/ip4/198.51.100.1/tcp/4001/p2p/" + private.String() + "/p2p-circuit"),
		m("/dns4/relay.example/tcp/4001/p2p/" + official.String() + "/p2p-circuit"),
	}
	set := map[peer.ID]bool{official: true}
	got := officialCircuits(addrs, set)
	if len(got) != 1 || !got[0].Equal(addrs[3]) {
		t.Fatalf("kept %v", got)
	}
	g := officialGater{official: set, coordinator: ids[1]}
	switch {
	case g.InterceptAddrDial(node, addrs[0]), g.InterceptAddrDial(node, addrs[1]):
		t.Fatal("the gater lets the auditor dial a node directly")
	case g.InterceptAddrDial(node, addrs[2]):
		t.Fatal("the gater lets the auditor dial through a private relay")
	case !g.InterceptAddrDial(node, addrs[3]):
		t.Fatal("the gater refuses an official circuit")
	case !g.InterceptAddrDial(official, m("/ip4/192.0.2.1/tcp/4001")):
		t.Fatal("the gater refuses the official relay itself")
	case g.InterceptAccept(nil):
		t.Fatal("an audit host accepts connections")
	}
}

func TestParsePrompts(t *testing.T) {
	cs, err := ParsePrompts(`# private pool
Explain tides.

{"system":"Be brief.","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello!"},{"role":"user","content":"Why is the sky blue?"}]}`)
	if err != nil || len(cs) != 2 {
		t.Fatalf("%+v %v", cs, err)
	}
	if ms := cs[1].messages(); len(ms) != 4 || ms[0].Role != "system" || ms[3].Content != "Why is the sky blue?" {
		t.Fatalf("%+v", ms)
	}
	for _, bad := range []string{`{"messages":[]}`, `{"messages":[{"role":"assistant","content":"x"}]}`, `{"messages":[{"role":"tool","content":"x"},{"role":"user","content":"y"}]}`, `{bad`} {
		if _, err := ParsePrompts(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestBuiltinQuestionsVaryAndAreOpen(t *testing.T) {
	r := mrand.New(mrand.NewPCG(1, 1))
	seen := map[string]bool{}
	zh, sys, turns := 0, 0, 0
	for range 200 {
		c := builtinChallenge(r)
		ms := c.messages()
		last := ms[len(ms)-1]
		if last.Role != "user" || strings.Contains(last.Content, "%") {
			t.Fatalf("bad question %+v", ms)
		}
		seen[last.Content] = true
		if c.System != "" {
			sys++
		}
		if len(c.Messages) > 1 {
			turns++
		}
		if strings.ContainsFunc(last.Content, func(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF }) {
			zh++
		}
	}
	if len(seen) < 150 || zh < 60 || zh > 140 || sys < 30 || turns < 15 {
		t.Fatalf("distinct %d, Chinese %d, with system %d, multi-turn %d of 200", len(seen), zh, sys, turns)
	}
}

func TestQuantiles(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	if quantile(xs, 0) != 1 || quantile(xs, 0.5) != 3 || quantile(xs, 1) != 5 || quantile(xs, 0.25) != 2 {
		t.Fatal("quantile")
	}
	ms := kMeans([]float64{0.9, 1}, 5, 1000, mrand.New(mrand.NewPCG(1, 2)))
	if ms[0] < 0.9 || ms[len(ms)-1] > 1 || ms[0] > ms[len(ms)-1] {
		t.Fatal("kMeans")
	}
}
