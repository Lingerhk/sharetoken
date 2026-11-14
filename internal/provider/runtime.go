package provider

import (
	"errors"
	"net/http"
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
	if se.Status == 429 {
		status.RetryAt = se.RetryAt
		if !status.RetryAt.After(time.Now()) {
			status.RetryAt = time.Now().Add(time.Minute)
		}
	}
	providerStatuses.byID[id] = status
}

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
	return e
}
