package provider

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What a subscription login's own replies say of its usage windows. Claude
// (anthropic-ratelimit-unified-*) and the ChatGPT Codex backend
// (x-codex-primary-*, x-codex-secondary-*) put the account's 5-hour and
// weekly windows on every answer, a job's or a warm-up's, so they are kept
// as they pass: reading them costs no request. The daily warm-up
// (internal/warmup) decides by them whether a window is running.

// UsageWindow is one usage window of a subscription account.
type UsageWindow struct {
	Name string `json:"name"` // "5h" or "7d"
	// SpanSeconds is how long the window runs once started.
	SpanSeconds int64 `json:"span_seconds"`
	// Used is the share of the window spent, in percent (0–100).
	Used float64 `json:"used"`
	// ResetsAt is when the window ends; zero: not started, or not known.
	ResetsAt time.Time `json:"resets_at,omitzero"`
}

// Span is how long the window runs.
func (w UsageWindow) Span() time.Duration { return time.Duration(w.SpanSeconds) * time.Second }

// UsageLimits is what the last reply that told them said of an account's
// windows.
type UsageLimits struct {
	Windows []UsageWindow `json:"windows"`
	At      time.Time     `json:"at"`
	// Rejected: the vendor turned the account away for its allowance.
	Rejected bool `json:"rejected,omitempty"`
	// Overage: requests now go past the plan, on extra usage or credits.
	Overage bool `json:"overage,omitempty"`
}

var usageSeen = struct {
	sync.Mutex
	limits  map[string]UsageLimits // by provider ID
	success map[string]time.Time   // last request that was answered, by provider ID
}{limits: map[string]UsageLimits{}, success: map[string]time.Time{}}

// UsageOf is what the replies through provider id last said of its
// windows; false when none said anything since this process started.
func UsageOf(id string) (UsageLimits, bool) {
	usageSeen.Lock()
	defer usageSeen.Unlock()
	l, ok := usageSeen.limits[id]
	if ok {
		l.Windows = append([]UsageWindow(nil), l.Windows...)
	}
	return l, ok
}

// LastSuccess is when a request through provider id was last answered in
// full (a job, a test or a warm-up); zero when none was since this process
// started. Any answered request starts a subscription's windows.
func LastSuccess(id string) time.Time {
	usageSeen.Lock()
	defer usageSeen.Unlock()
	return usageSeen.success[id]
}

func noteSuccess(id string, at time.Time) {
	if id == "" {
		return
	}
	usageSeen.Lock()
	usageSeen.success[id] = at
	usageSeen.Unlock()
}

func noteUsage(id string, l UsageLimits) {
	if id == "" {
		return
	}
	usageSeen.Lock()
	usageSeen.limits[id] = l
	usageSeen.Unlock()
}

func forgetUsage(id string) {
	usageSeen.Lock()
	delete(usageSeen.limits, id)
	delete(usageSeen.success, id)
	usageSeen.Unlock()
}

// limitsRecorder keeps the windows the answers of owner's requests tell.
type limitsRecorder struct {
	inner httpDoer
	owner string
}

func (r *limitsRecorder) Do(req *http.Request) (*http.Response, error) {
	resp, err := r.inner.Do(req)
	if err == nil && resp != nil {
		if l, ok := usageFromHeaders(resp.Header, time.Now()); ok {
			noteUsage(r.owner, l)
		}
	}
	return resp, err
}

// UsageFromHeaders reads the windows a vendor reply's headers tell: Claude's
// (anthropic-ratelimit-unified-*) or the ChatGPT backend's (x-codex-*).
func UsageFromHeaders(h http.Header, now time.Time) (UsageLimits, bool) {
	return usageFromHeaders(h, now)
}

// usageFromHeaders reads the windows h tells, of either vendor.
func usageFromHeaders(h http.Header, now time.Time) (UsageLimits, bool) {
	if l, ok := claudeUsageHeaders(h, now); ok {
		return l, true
	}
	return codexUsageHeaders(h, now)
}

// claudeUsageHeaders reads anthropic-ratelimit-unified-<window>-utilization
// (a fraction) and -reset (Unix seconds), as Claude Code reads them.
func claudeUsageHeaders(h http.Header, now time.Time) (UsageLimits, bool) {
	l := UsageLimits{At: now, Windows: []UsageWindow{}}
	for _, w := range []struct {
		key, name string
		span      time.Duration
	}{{"5h", "5h", 5 * time.Hour}, {"7d", "7d", 7 * 24 * time.Hour}} {
		pre := "anthropic-ratelimit-unified-" + w.key + "-"
		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(pre+"utilization")), 64)
		if err != nil {
			continue
		}
		uw := UsageWindow{Name: w.name, SpanSeconds: int64(w.span / time.Second), Used: clampPercent(used * 100)}
		if at, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"reset")), 10, 64); err == nil && at > 0 {
			uw.ResetsAt = time.Unix(at, 0)
		}
		if strings.EqualFold(strings.TrimSpace(h.Get(pre+"status")), "rejected") {
			uw.Used = 100
		}
		l.Windows = append(l.Windows, uw)
	}
	status := strings.ToLower(strings.TrimSpace(h.Get("anthropic-ratelimit-unified-status")))
	if len(l.Windows) == 0 && status == "" {
		return l, false
	}
	l.Rejected = status == "rejected"
	claim := strings.ToLower(h.Get("anthropic-ratelimit-unified-representative-claim"))
	l.Overage = strings.Contains(claim, "overage") ||
		strings.EqualFold(strings.TrimSpace(h.Get("anthropic-ratelimit-unified-overage-in-use")), "true")
	return l, true
}

// codexUsageHeaders reads x-codex-primary-* and x-codex-secondary-*:
// used-percent, window-minutes, and reset-at (Unix seconds) or
// reset-after-seconds, as Codex reads them. When x-codex-active-limit names
// the image allowance they are its window, not the account's.
func codexUsageHeaders(h http.Header, now time.Time) (UsageLimits, bool) {
	if strings.Contains(strings.ToLower(h.Get("x-codex-active-limit")), "image") {
		return UsageLimits{}, false
	}
	l := UsageLimits{At: now, Windows: []UsageWindow{}}
	for _, which := range []string{"primary", "secondary"} {
		pre := "x-codex-" + which + "-"
		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(pre+"used-percent")), 64)
		if err != nil {
			continue
		}
		mins, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"window-minutes")), 10, 64)
		if err != nil || mins <= 0 {
			continue
		}
		w := UsageWindow{Name: spanName(time.Duration(mins) * time.Minute), SpanSeconds: mins * 60, Used: clampPercent(used)}
		if at, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"reset-at")), 10, 64); err == nil && at > 0 {
			w.ResetsAt = time.Unix(at, 0)
		} else if after, err := strconv.ParseInt(strings.TrimSpace(h.Get(pre+"reset-after-seconds")), 10, 64); err == nil && after > 0 {
			w.ResetsAt = now.Add(time.Duration(after) * time.Second).Round(time.Second)
		}
		l.Windows = append(l.Windows, w)
	}
	if len(l.Windows) == 0 {
		return l, false
	}
	return l, true
}

// spanName names a window by how long it runs: "5h", "7d".
func spanName(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	}
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
}

func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}
