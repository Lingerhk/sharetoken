package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenRefreshAcrossProcesses(t *testing.T) {
	home := t.TempDir()
	e := parityEntry()
	e.OAuth.RefreshToken = "refresh"
	e.OAuth.ExpiresAt = time.Now().Add(time.Hour)
	if err := Update(home, func(f *File) error { return f.Put(e) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		commands[i] = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTokenRefreshProcessHelper$")
		commands[i].Env = append(os.Environ(), "SHARETOKEN_TEST_REFRESH_HOME="+home)
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, "refresh-count"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "refresh\n" {
		t.Fatalf("refresh happened more than once: %q", raw)
	}
}

func TestTokenRefreshProcessHelper(t *testing.T) {
	home := os.Getenv("SHARETOKEN_TEST_REFRESH_HOME")
	if home == "" {
		t.Skip("subprocess helper")
	}
	flow := *oauthFlows[KindAnthropic]
	flow.refresh = func(context.Context, *Token) (*Token, error) {
		f, err := os.OpenFile(filepath.Join(home, "refresh-count"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString("refresh\n")
		f.Close()
		return &Token{AccessToken: "renewed", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, err
	}
	oauthFlows[KindAnthropic] = &flow
	s := (&Tokens{Home: home}).For("claude").(*tokenSource)
	tok, err := s.RefreshRejected(context.Background(), "access")
	if err != nil || !strings.EqualFold(tok.AccessToken, "renewed") {
		t.Fatalf("renewal failed: %v", err)
	}
}
