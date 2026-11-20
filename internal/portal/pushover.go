package portal

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Pushover alerts for the administrators. Config.PushoverFile names a file
// with PUSHOVER_TOKEN=… and PUSHOVER_USER=… lines (the format the relay
// monitor reads); it is read for each alert, so it can be added or changed
// without a restart. Without it alerts only go to the log.

const (
	pushoverAPI     = "https://api.pushover.net/1/messages.json"
	pushoverTimeout = 15 * time.Second
	pushoverMaxText = 1024 // Pushover's limit on a message
)

// pushoverCreds reads the token and user key, or returns ok false.
func pushoverCreds(path string) (token, user string, ok bool) {
	if path == "" {
		return "", "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, found := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !found || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		switch strings.TrimSpace(k) {
		case "PUSHOVER_TOKEN":
			token = v
		case "PUSHOVER_USER":
			user = v
		}
	}
	return token, user, token != "" && user != ""
}

// push sends an alert in the background; it never blocks the request that
// raised it. priority is Pushover's: -1 quiet, 0 normal, 1 high.
func (s *Server) push(title, message string, priority int) {
	token, user, ok := pushoverCreds(s.cfg.PushoverFile)
	if !ok {
		s.log.Warn("no Pushover account configured (alerts.pushover_file): alert only logged", "title", title)
		return
	}
	if r := []rune(message); len(r) > pushoverMaxText {
		message = string(r[:pushoverMaxText-1]) + "…"
	}
	api := s.pushoverURL
	if api == "" {
		api = pushoverAPI
	}
	form := url.Values{"token": {token}, "user": {user}, "title": {title}, "message": {message}, "priority": {strconv.Itoa(priority)}}
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		ctx, cancel := context.WithTimeout(context.Background(), pushoverTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, strings.NewReader(form.Encode()))
		if err != nil {
			s.log.Warn("pushover", "err", err)
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			s.log.Warn("pushover: alert not sent", "title", title, "err", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			s.log.Warn("pushover: alert refused", "title", title, "status", resp.Status)
			return
		}
		s.log.Info("pushover: alert sent", "title", title)
	}()
}
