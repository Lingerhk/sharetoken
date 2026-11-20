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

// A 429 is not always about the account. Some say that this one request
// can never fit the account's allowance however long it waits: OpenAI's
// "Request too large for gpt-4o in organization … on tokens per min (TPM):
// Limit 30000, Requested 45000. The input or output tokens must be reduced
// in order to run successfully.", or a prompt past the model's context told
// with a 429. Resting the provider for those would turn every other buyer
// away for one buyer's request (magpie LESSONS: decide whether a refusal is
// about the account, the model or the request before resting the account).
// Such a 429 fails its job alone: a request too large becomes a 413, one too
// long for the context a 400 (which the buyer tells its client in the words
// the client compacts on); neither rests the provider. Every other 429 (a
// quota or plan used up, a rate limit) is the account's, as before.
var (
	requestTooLargePattern = regexp.MustCompile(`(?i)request too large|(?:input|output|prompt)(?: or output)? tokens must be reduced|\b(?:this |the |your )?(?:request|prompt|input)\b(?:'s)?(?: \w+){0,3} (?:exceeds?|is larger than|is greater than|is more than)(?: the| your)?(?: \w+){0,3} (?:tpm|tokens? per min(?:ute)?|per.?minute token limit)|单次请求.{0,20}超(?:过|出)`)
	limitRequestedPattern  = regexp.MustCompile(`(?i)\blimit:? ?(\d+), requested:? ?(\d+)`)
	contextPattern         = regexp.MustCompile(`(?i)context_length_exceeded|context_too_large|prompt is too long|input is too long|maximum context length|context (?:length|window|limit) (?:exceeded|is exceeded)|(?:exceeds?|exceeded) (?:the )?(?:model'?s? )?(?:maximum )?context|longer than the model'?s? context length|上下文(?:长度)?(?:超|过长)|超(?:过|出)(?:了)?(?:模型)?(?:的)?(?:最大)?上下文`)
	pacePattern            = regexp.MustCompile(`(?i)rate.?limit|per (?:minute|hour|day)|\bTP[MD]\b|please wait|try again later|throttl|速率|频繁|限流|每(?:秒|分钟|小时|天|日)`)
)

// requestLimited is err with a 429 that is about the request itself, not the
// account, turned into the request error it is (see requestTooLargePattern).
// Any other error comes back as it was.
func requestLimited(err error) error {
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		return err
	}
	out := *se
	out.RetryAt = time.Time{} // waiting does not help
	switch {
	case requestTooLarge(se.Body):
		out.Status = http.StatusRequestEntityTooLarge
	case contextPattern.MatchString(se.Body) && !pacePattern.MatchString(se.Body):
		out.Status = http.StatusBadRequest
	default:
		return err
	}
	return &out
}

// requestTooLarge reports whether a 429's body says the request alone is
// larger than the account's allowance: in words, or as a limit smaller than
// what was requested with nothing used ("Limit 30000, Requested 45000"; a
// rate limit says "Limit 30000, Used 28000, Requested 5000").
func requestTooLarge(body string) bool {
	if requestTooLargePattern.MatchString(body) {
		return true
	}
	m := limitRequestedPattern.FindStringSubmatch(body)
	if m == nil {
		return false
	}
	limit, _ := strconv.ParseUint(m[1], 10, 64)
	asked, _ := strconv.ParseUint(m[2], 10, 64)
	return asked > limit
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
