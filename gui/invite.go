package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Invite links. The app registers the sharetoken:// scheme with the system
// (Info.plist, the NSIS installer, the .desktop file), so an invite's page on
// the portal can open it with sharetoken://p/<token>?h=<portal> (or /s/). The
// link arrives as an argument (Windows, Linux: of this process, or of a second
// start, which the single-instance lock hands over) or as an Apple Event
// (macOS). It goes to the daemon (POST /api/v1/invite/pending), which keeps it
// until the page shows it and the user accepts or dismisses it; the window
// comes forward. Nothing is accepted here.

const inviteScheme = "sharetoken:"

// inviteArg returns the first argument that is a sharetoken: link.
func inviteArg(args []string) string {
	for _, a := range args {
		if len(a) > len(inviteScheme) && strings.EqualFold(a[:len(inviteScheme)], inviteScheme) {
			return a
		}
	}
	return ""
}

// inviteForwarder hands invite links to the daemon, once each.
type inviteForwarder struct {
	start func() error
	ep    func() endpoint
	show  func()

	mu   sync.Mutex
	last string
	at   time.Time
}

// forward passes link on in the background and shows the window. The same
// link twice within a few seconds is one click: Windows and Linux report a
// first start's argument both as an argument and as an event.
func (f *inviteForwarder) forward(link string) {
	if link == "" {
		return
	}
	f.mu.Lock()
	dup := link == f.last && time.Since(f.at) < 5*time.Second
	f.last, f.at = link, time.Now()
	f.mu.Unlock()
	if dup {
		return
	}
	go func() {
		if err := f.send(link); err != nil {
			log.Printf("sharetoken-gui: passing an invite link to the daemon: %v", err)
		}
		if f.show != nil {
			f.show()
		}
	}()
}

func (f *inviteForwarder) send(link string) error {
	var last error
	for i := 0; i < 20; i++ {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		if err := f.start(); err != nil {
			last = err
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		body, _ := json.Marshal(map[string]string{"link": link})
		req, err := f.ep().request(ctx, http.MethodPost, "/api/v1/invite/pending", bytes.NewReader(body))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			last = err
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		cancel()
		switch {
		case resp.StatusCode == http.StatusOK:
			return nil
		case resp.StatusCode == http.StatusBadRequest:
			return fmt.Errorf("the daemon refused %q: %s", link, strings.TrimSpace(string(msg)))
		}
		last = fmt.Errorf("the daemon answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return last
}
