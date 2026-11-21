package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	// /usage names its resets in a zone; Windows has no zone database
	_ "time/tzdata"

	"sharetoken/internal/provider"
)

// Reading the local agents' usage windows without spending any of them:
// Claude Code's /usage, which it answers itself without a model, and for
// Codex the ChatGPT backend's usage endpoint with the CLI's own sign-in,
// read only (the token is never renewed here: that is the CLI's to do).

var errUnknown = errors.New("the usage could not be read")

// readClaude runs Claude Code's /usage and reads its windows.
func readClaude(ctx context.Context, run runner, bin string, now time.Time) (provider.UsageLimits, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var out []byte
	err := inTemp(func(dir string) error {
		var err error
		var errOut []byte
		out, errOut, err = run(ctx, command{path: bin, args: []string{"-p", "--strict-mcp-config", "--setting-sources", "", "/usage"}, dir: dir, env: agentEnv(bin)})
		if err != nil && len(strings.TrimSpace(string(out))) == 0 {
			if msg := lastLines(errOut); msg != "" {
				return fmt.Errorf("Claude Code /usage: %s", clip(msg))
			}
			return err
		}
		return nil
	})
	if err != nil {
		return provider.UsageLimits{}, err
	}
	return parseClaudeUsage(string(out), now)
}

// "Current session: 34% used · resets Nov 20 at 11:50am (Asia/Singapore)"
// "Current week (all models): 32% used · resets Nov 26 at 8am (Asia/Singapore)"
// "Current week (Fable): 62% used · …" is one model's, not the account's.
var claudeUsageLine = regexp.MustCompile(`^Current (session|week)(?: \(([^)]+)\))?:\s*([0-9.]+)% used(?:\s*·\s*resets (.+))?$`)

var claudeUsageDenied = regexp.MustCompile(`(?i)not (logged|signed) in|please run /login|invalid api key|sign-in (has )?expired|unauthorized|\b401\b`)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// parseClaudeUsage reads /usage's windows; now dates a reset that names no
// year.
func parseClaudeUsage(text string, now time.Time) (provider.UsageLimits, error) {
	l := provider.UsageLimits{At: now, Windows: []provider.UsageWindow{}}
	text = ansi.ReplaceAllString(text, "")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if claudeUsageDenied.MatchString(line) {
			return l, errors.New("Claude Code: " + clip(line))
		}
		if strings.Contains(strings.ToLower(line), "using your overages") {
			l.Overage = true
		}
		m := claudeUsageLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == "week" && m[2] != "" && !strings.EqualFold(m[2], "all models") {
			continue
		}
		used, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			continue
		}
		w := provider.UsageWindow{Name: "5h", SpanSeconds: int64(5 * time.Hour / time.Second), Used: used}
		if m[1] == "week" {
			w.Name, w.SpanSeconds = "7d", int64(7*24*time.Hour/time.Second)
		}
		if m[4] != "" {
			if t, ok := parseReset(m[4], now); ok {
				w.ResetsAt = t
			}
		}
		l.Windows = append(l.Windows, w)
	}
	if len(l.Windows) == 0 {
		return l, errUnknown
	}
	return l, nil
}

var resetZone = regexp.MustCompile(`\s*\(([^)]+)\)\s*$`)

// parseReset reads "Nov 20 at 11:50am (Asia/Singapore)", "Nov 26 at 8am",
// "Nov 20, 10:30pm", "11:50am" (today), in the zone it names, else the
// machine's.
func parseReset(s string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	if m := resetZone.FindStringSubmatch(s); m != nil {
		if z, err := time.LoadLocation(strings.TrimSpace(m[1])); err == nil {
			loc = z
		}
		s = s[:len(s)-len(m[0])]
	}
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	local := now.In(loc)
	// "Nov 20 at 11:50am" on macOS, "Nov 20, 10:30pm" on Linux and Windows
	// (the comma is dropped above), and a 24-hour clock where the system has one
	for _, f := range []string{"Jan 2 at 3:04pm", "Jan 2 at 3pm", "Jan 2 3:04pm", "Jan 2 3pm", "Jan 2 at 15:04", "Jan 2 15:04",
		"Jan 2 2006 at 3:04pm", "Jan 2 2006 at 3pm", "Jan 2 2006 3:04pm", "Jan 2 2006 3pm", "3:04pm", "3pm", "15:04"} {
		t, err := time.ParseInLocation(f, s, loc)
		if err != nil {
			continue
		}
		switch {
		case !strings.Contains(f, "Jan"):
			t = time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(local.Add(-time.Minute)) {
				t = t.AddDate(0, 0, 1)
			}
		case !strings.Contains(f, "2006"):
			t = time.Date(local.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(local.Add(-24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
		}
		return t, true
	}
	return time.Time{}, false
}

// codexAuth is the part of the Codex CLI's auth.json read here.
type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func codexHome() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// codexUsageURL is the ChatGPT backend's usage endpoint; tests swap it.
var codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// readCodex reads the ChatGPT account's windows with the Codex CLI's own
// sign-in. A sign-in kept in the system keychain, or an expired token, is
// read as unknown.
func readCodex(ctx context.Context, now time.Time) (provider.UsageLimits, error) {
	data, err := os.ReadFile(filepath.Join(codexHome(), "auth.json"))
	if err != nil {
		return provider.UsageLimits{}, errUnknown
	}
	var a codexAuth
	if json.Unmarshal(data, &a) != nil || a.Tokens == nil || a.Tokens.AccessToken == "" {
		return provider.UsageLimits{}, errUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL, nil)
	if err != nil {
		return provider.UsageLimits{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Tokens.AccessToken)
	if a.Tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", a.Tokens.AccountID)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/0.169.0")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return provider.UsageLimits{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return provider.UsageLimits{}, errUnknown // the CLI renews its token when it next runs
	}
	if resp.StatusCode != http.StatusOK {
		return provider.UsageLimits{}, fmt.Errorf("ChatGPT usage: HTTP %d", resp.StatusCode)
	}
	return parseCodexUsage(body, now)
}

type codexWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	LimitWindowSecs int64   `json:"limit_window_seconds"`
	ResetAfterSecs  int64   `json:"reset_after_seconds"`
	ResetAt         int64   `json:"reset_at"`
}

// parseCodexUsage reads /wham/usage:
// {"rate_limit":{"allowed":true,"primary_window":{…},"secondary_window":{…}},"credits":{…}}
func parseCodexUsage(body []byte, now time.Time) (provider.UsageLimits, error) {
	var u struct {
		RateLimit struct {
			Allowed   *bool        `json:"allowed"`
			Primary   *codexWindow `json:"primary_window"`
			Secondary *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		Credits *struct {
			Has       bool `json:"has_credits"`
			Unlimited bool `json:"unlimited"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return provider.UsageLimits{}, fmt.Errorf("ChatGPT usage: %w", err)
	}
	l := provider.UsageLimits{At: now, Windows: []provider.UsageWindow{}}
	for _, w := range []*codexWindow{u.RateLimit.Primary, u.RateLimit.Secondary} {
		if w == nil || w.LimitWindowSecs <= 0 {
			continue
		}
		span := time.Duration(w.LimitWindowSecs) * time.Second
		uw := provider.UsageWindow{Name: "5h", SpanSeconds: w.LimitWindowSecs, Used: min(max(w.UsedPercent, 0), 100)}
		if span >= 24*time.Hour {
			uw.Name = "7d"
		}
		if w.ResetAt > 0 {
			uw.ResetsAt = time.Unix(w.ResetAt, 0)
		} else if w.ResetAfterSecs > 0 {
			uw.ResetsAt = now.Add(time.Duration(w.ResetAfterSecs) * time.Second).Round(time.Second)
		}
		l.Windows = append(l.Windows, uw)
	}
	credits := u.Credits != nil && (u.Credits.Has || u.Credits.Unlimited)
	if u.RateLimit.Allowed != nil && !*u.RateLimit.Allowed {
		if credits {
			l.Overage = true
		} else {
			l.Rejected = true
		}
	}
	if len(l.Windows) == 0 {
		return l, errUnknown
	}
	return l, nil
}
