package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/node"
)

type RuntimeStatus struct {
	Status    int       `json:"status,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	RetryAt   time.Time `json:"retry_at,omitzero"`
}

var providerStatuses = struct {
	sync.Mutex
	byID map[string]RuntimeStatus
}{byID: map[string]RuntimeStatus{}}

func providerStatus(id string) RuntimeStatus {
	providerStatuses.Lock()
	defer providerStatuses.Unlock()
	return providerStatuses.byID[id]
}

func recordProviderStatus(id string, err error) {
	if id == "" {
		return
	}
	providerStatuses.Lock()
	defer providerStatuses.Unlock()
	if err == nil {
		delete(providerStatuses.byID, id)
		return
	}
	var se *node.StatusError
	if !errors.As(err, &se) {
		return
	}
	status := RuntimeStatus{Status: se.Status, RequestID: se.RequestID}
	switch se.Status {
	case http.StatusTooManyRequests:
		status.RetryAt = se.RetryAt
		if !status.RetryAt.After(time.Now()) {
			status.RetryAt = time.Now().Add(time.Minute)
		}
	case http.StatusUnauthorized:
		// The key or the login is no longer taken (a login was renewed and
		// tried again before this): every job would fail the same way until
		// the user signs in again, which clears this.
		status.RetryAt = time.Now().Add(refusedCooldown)
	}
	providerStatuses.byID[id] = status
}

const (
	refusedCooldown = 10 * time.Minute
	// maxCooldown: no limit a provider has resets later (the longest is a week).
	maxCooldown = 8 * 24 * time.Hour
)

func responseStatus(resp *http.Response, body string) *node.StatusError {
	e := &node.StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(body), RequestID: clip(resp.Header.Get("request-id"), 120)}
	if e.RequestID == "" {
		e.RequestID = clip(resp.Header.Get("x-request-id"), 120)
	}
	if raw := resp.Header.Get("Retry-After"); raw != "" {
		if secs, err := strconv.ParseInt(raw, 10, 64); err == nil && secs >= 0 {
			e.RetryAt = time.Now().Add(time.Duration(min(secs, 86400)) * time.Second)
		} else if at, err := http.ParseTime(raw); err == nil {
			e.RetryAt = at
		}
	}
	if e.Status == http.StatusTooManyRequests && e.RetryAt.IsZero() {
		e.RetryAt = limitReset(resp.Header, body, time.Now())
	}
	return e
}

var retryInPattern = regexp.MustCompile(`(?i)retry in ([0-9]+(?:\.[0-9]+)?)s`)

// limitReset reads when a rate limit ends from a 429 that has no Retry-After,
// the way each provider tells it (zero when it does not):
//
//	Anthropic   anthropic-ratelimit-unified-reset: <unix seconds> (a subscription's window)
//	Codex       x-codex-primary-used-percent: 100 with x-codex-primary-reset-after-seconds
//	            (and -secondary-), or {"error": {"type": "usage_limit_reached",
//	            "resets_at": <unix seconds>, "resets_in_seconds": n}}
//	Google      {"error": {"details": [{"metadata": {"quotaResetDelay": "1h2m3s"}},
//	            {"@type": "…google.rpc.RetryInfo", "retryDelay": "39s"}]}}, or
//	            "Please retry in 39.5s" in the message
//
// Without it a provider out of its quota for hours is asked again every
// minute, and each time a buyer's job fails.
func limitReset(h http.Header, body string, now time.Time) time.Time {
	in := func(d time.Duration) time.Time {
		if d <= 0 {
			return time.Time{}
		}
		return now.Add(min(d, maxCooldown))
	}
	seconds := func(s string) time.Duration {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || f <= 0 {
			return 0
		}
		return time.Duration(min(f, maxCooldown.Seconds()) * float64(time.Second))
	}
	if at, err := strconv.ParseInt(strings.TrimSpace(h.Get("anthropic-ratelimit-unified-reset")), 10, 64); err == nil && at > 0 {
		return in(time.Unix(at, 0).Sub(now))
	}
	var codex time.Duration
	for _, window := range []string{"primary", "secondary"} {
		if used, err := strconv.ParseFloat(h.Get("x-codex-"+window+"-used-percent"), 64); err == nil && used >= 100 {
			codex = max(codex, seconds(h.Get("x-codex-"+window+"-reset-after-seconds")))
		}
	}
	if codex > 0 {
		return in(codex)
	}
	var doc struct {
		Error struct {
			Type            string      `json:"type"`
			ResetsAt        json.Number `json:"resets_at"`
			ResetsInSeconds json.Number `json:"resets_in_seconds"`
			Details         []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
				Metadata   struct {
					QuotaResetDelay string `json:"quotaResetDelay"`
				} `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	raw := strings.TrimSpace(body)
	if strings.HasPrefix(raw, "[") { // Code Assist answers with a list of one
		var docs []json.RawMessage
		if json.Unmarshal([]byte(raw), &docs) == nil && len(docs) > 0 {
			raw = string(docs[0])
		}
	}
	if json.Unmarshal([]byte(raw), &doc) == nil {
		switch doc.Error.Type {
		case "usage_limit_reached", "rate_limit_exceeded":
			if at, err := doc.Error.ResetsAt.Int64(); err == nil && at > 0 {
				return in(time.Unix(at, 0).Sub(now))
			}
			if d := seconds(doc.Error.ResetsInSeconds.String()); d > 0 {
				return in(d)
			}
		}
		for _, d := range doc.Error.Details {
			delay := d.Metadata.QuotaResetDelay
			if delay == "" && strings.HasSuffix(d.Type, "google.rpc.RetryInfo") {
				delay = d.RetryDelay
			}
			if dur, err := time.ParseDuration(delay); err == nil && dur > 0 {
				return in(dur)
			}
		}
	}
	if m := retryInPattern.FindStringSubmatch(body); m != nil {
		return in(seconds(m[1]))
	}
	return time.Time{}
}
