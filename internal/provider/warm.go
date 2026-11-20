package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// Warming up a subscription login: a Claude or ChatGPT window starts at the
// account's first request after it resets, not at the reset. One tiny
// request at a time the seller chooses starts it then (internal/warmup
// decides when). The request goes the way a job's does — the same
// Upstream, so the same request profile, proxy, TLS and token renewal —
// but it is no job: no node, no buyer, no ledger.

// WarmOptions is a provider's warm-up: times of day to start its 5-hour
// window at, and whether to start a window as soon as it resets.
type WarmOptions struct {
	// Times are local times of day, "06:00", earliest first.
	Times []string `json:"times,omitempty"`
	// OnReset starts windows as they reset: "" no, "week" the weekly one,
	// "all" the 5-hour one too.
	OnReset string `json:"on_reset,omitempty"`
	// Model is the model to ask; empty: the cheapest known one.
	Model string `json:"model,omitempty"`
}

// MaxWarmTimes is how many times of day a warm-up may have.
const MaxWarmTimes = 6

// OnReset values.
const (
	WarmOnResetWeek = "week"
	WarmOnResetAll  = "all"
)

func (w *WarmOptions) normalize() error {
	ts, err := NormalizeTimes(w.Times)
	if err != nil {
		return err
	}
	w.Times = ts
	switch w.OnReset {
	case "", WarmOnResetWeek, WarmOnResetAll:
	case "off":
		w.OnReset = ""
	default:
		return fmt.Errorf("on_reset must be off, week or all, not %q", w.OnReset)
	}
	w.Model = strings.TrimSpace(w.Model)
	return nil
}

func (w *WarmOptions) empty() bool {
	return w == nil || len(w.Times) == 0 && w.OnReset == "" && w.Model == ""
}

// NormalizeTimes checks times of day ("6:00", "06:00"), and returns them as
// "15:04", sorted, each once.
func NormalizeTimes(in []string) ([]string, error) {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		h, m, ok := ParseClock(s)
		if !ok {
			return nil, fmt.Errorf("a time of day must look like 06:00, not %q", s)
		}
		t := fmt.Sprintf("%02d:%02d", h, m)
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > MaxWarmTimes {
		return nil, fmt.Errorf("at most %d times of day", MaxWarmTimes)
	}
	slices.Sort(out)
	return out, nil
}

// ParseClock reads "H:MM" or "HH:MM".
func ParseClock(s string) (hour, minute int, ok bool) {
	hs, ms, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found || len(hs) < 1 || len(hs) > 2 || len(ms) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// WarmCapable says whether e is a subscription login that has usage windows
// to start: a Claude or a ChatGPT (Codex) login.
func (e *Entry) WarmCapable() bool {
	return e.Auth == AuthOAuth && (e.Type == KindAnthropic || e.Type == KindOpenAI)
}

// ErrNotWarmable is returned for a provider that has no windows to warm up.
var ErrNotWarmable = errors.New("only a Claude or ChatGPT subscription login has usage windows to warm up")

// SetWarmup sets a provider's warm-up; nil or an empty one turns it off.
func SetWarmup(home, name string, w *WarmOptions) (View, error) {
	var v View
	return v, Update(home, func(f *File) error {
		e, err := f.Find(name)
		if err != nil {
			return err
		}
		if w != nil {
			c := *w
			if err := c.normalize(); err != nil {
				return err
			}
			w = &c
		}
		if w.empty() {
			w = nil
		} else if !e.WarmCapable() {
			return ErrNotWarmable
		}
		e.Warmup = w
		v = e.View()
		return nil
	})
}

// WarmResult is what a warm-up did.
type WarmResult struct {
	Model string `json:"model"`
}

// Warm sends provider name the warm-up's one tiny request. It tries the
// cheapest models first and goes on to the next when one is refused as a
// model the account doesn't have.
func Warm(ctx context.Context, home, name string) (WarmResult, error) {
	var r WarmResult
	return r, With(home, name, func(e *Entry, ts TokenSource) error {
		if !e.WarmCapable() {
			return ErrNotWarmable
		}
		up, err := e.Upstream(ts)
		if err != nil {
			return err
		}
		for _, m := range warmModels(e) {
			if err = warmProbe(ctx, up, m); err == nil {
				r.Model = m
				return nil
			}
			if !modelRefused(err) {
				return err
			}
		}
		return err
	})
}

// warmModels are the models a warm-up asks, cheapest first: the one the
// seller set, then the family's small one, then what the provider sells.
func warmModels(e *Entry) []string {
	var out []string
	add := func(m string) {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	if e.Warmup != nil {
		add(e.Warmup.Model)
	}
	small := "mini"
	if e.Type == KindAnthropic {
		small = "haiku"
	}
	for _, m := range e.Models {
		if strings.Contains(strings.ToLower(m.UpstreamModel), small) {
			add(m.UpstreamModel)
		}
	}
	if e.Type == KindAnthropic {
		add("claude-haiku-4-5-20251001")
		add("claude-haiku-5-5")
	} else {
		for _, m := range codexModels {
			if strings.Contains(m, "mini") {
				add(m)
			}
		}
	}
	for _, m := range e.Models {
		add(m.UpstreamModel)
	}
	if len(out) == 0 && e.Type == KindOpenAI && len(codexModels) > 0 {
		add(codexModels[0])
	}
	return out
}

// warmProbe asks up for one short answer to "hi" and reads it to its end.
func warmProbe(ctx context.Context, up node.Upstream, model string) error {
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": true, "max_tokens": 16,
		"stream_options": map[string]bool{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": "hi"}},
	})
	chunks := 0
	err := up.Stream(ctx, body, func(payload []byte) error {
		if _, err := protocol.ParseChunk(payload); err == nil {
			chunks++
		}
		return nil
	})
	if err == nil && chunks == 0 {
		err = errors.New("the provider answered with an empty stream")
	}
	return err
}

// modelRefused says err is the vendor turning the model away (an account
// without it), not the account or the request.
func modelRefused(err error) bool {
	var se *node.StatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.Status {
	case http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		return strings.Contains(strings.ToLower(se.Body), "model")
	}
	return false
}
