package mailer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	netmail "net/mail"
	"time"

	"github.com/resend/resend-go/v2"
)

// APITransport sends through a provider's HTTP API. The provider builds the
// message and signs it with its own DKIM key, so the worker hands it the
// fields instead of a signed message.
type APITransport interface {
	Name() string
	// Send returns the provider's id for the message. A *PermanentError means
	// retrying is pointless; any other error is retried. messageID is stable
	// across retries of one message.
	Send(ctx context.Context, m *OutMail, messageID string) (remote string, err error)
}

// Resend sends through resend.com (docs/mail.md). Its port is 443, which
// hosting networks that block outbound SMTP still allow.
type Resend struct {
	client *resend.Client
}

// NewResend prepares a Resend transport. baseURL is for tests; empty means the
// real service.
func NewResend(apiKey, baseURL string) (*Resend, error) {
	hc := &http.Client{Timeout: time.Minute, Transport: statusRecorder{http.DefaultTransport}}
	c := resend.NewCustomClient(hc, apiKey)
	if baseURL != "" {
		if err := c.BaseURL.UnmarshalBinary([]byte(baseURL)); err != nil {
			return nil, err
		}
	}
	return &Resend{client: c}, nil
}

// Name implements APITransport.
func (r *Resend) Name() string { return "resend" }

// The SDK reports only a message for most failures; the status code decides
// whether to retry, so the HTTP client records it for the caller.
type statusKey struct{}

type statusRecorder struct{ next http.RoundTripper }

func (s statusRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.next.RoundTrip(req)
	if p, ok := req.Context().Value(statusKey{}).(*int); ok && resp != nil {
		*p = resp.StatusCode
	}
	return resp, err
}

// Send implements APITransport.
func (r *Resend) Send(ctx context.Context, m *OutMail, messageID string) (string, error) {
	from, err := netmail.ParseAddress(headerText(m.From))
	if err != nil {
		return "", &PermanentError{Err: fmt.Errorf("sender %q: %w", m.From, err)}
	}
	to, err := netmail.ParseAddress(headerText(m.To))
	if err != nil {
		return "", &PermanentError{Err: fmt.Errorf("recipient %q: %w", m.To, err)}
	}
	req := &resend.SendEmailRequest{
		From: from.String(), To: []string{to.Address}, Subject: headerText(m.Subject),
		Text: m.Text, Html: m.HTML, Headers: map[string]string{},
	}
	if m.ReplyTo != "" {
		if rt, err := netmail.ParseAddress(headerText(m.ReplyTo)); err == nil {
			req.ReplyTo = rt.String()
		}
	}
	if id := headerText(m.InReplyTo); id != "" {
		req.Headers["In-Reply-To"] = id
		req.Headers["References"] = id
	}
	if m.Kind == KindVerify || m.Kind == KindNotice {
		req.Headers["Auto-Submitted"] = "auto-generated"
	}
	if len(req.Headers) == 0 {
		req.Headers = nil
	}
	status := 0
	ctx = context.WithValue(ctx, statusKey{}, &status)
	// The key makes a retry after a lost response a no-op instead of a second copy.
	sent, err := r.client.Emails.SendWithOptions(ctx, req, &resend.SendEmailOptions{IdempotencyKey: messageID})
	if err != nil {
		err = fmt.Errorf("resend: %w", err)
		// 400 and 422 refuse the message itself (a bad address, say). 401 and 403
		// are the key or the domain's verification: fixing those should not lose
		// mail, so they are retried like 429 and 5xx.
		if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
			return "resend", &PermanentError{Remote: "resend", Err: err}
		}
		if status != 0 {
			err = fmt.Errorf("%w (HTTP %d)", err, status)
		}
		return "resend", err
	}
	if sent == nil || sent.Id == "" {
		return "resend", errors.New("resend: no id in the reply")
	}
	return "resend " + sent.Id, nil
}
