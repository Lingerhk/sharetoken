package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnthropicDiscoveryDistinguishesFallback(t *testing.T) {
	var status atomic.Int32
	status.Store(503)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		fmt.Fprint(w, `{"error":{"type":"unavailable"}}`)
	}))
	defer srv.Close()
	e := parityEntry()
	e.BaseURL = srv.URL
	e.ID = "discovery"
	r, err := e.DiscoverModels(context.Background(), FixedToken(e.OAuth))
	if err != nil || r.Source != "fallback" || r.Verification != "unverified" || !slices.Contains(r.Models, "claude-mythos-5-2") {
		t.Fatalf("fallback: %+v %v", r, err)
	}
	for _, code := range []int{401, 403} {
		status.Store(int32(code))
		_, err := e.DiscoverModels(context.Background(), FixedToken(e.OAuth))
		if !errors.Is(err, ErrKeyRefused) {
			t.Fatalf("masked HTTP %d: %v", code, err)
		}
	}
}

func TestProviderProxyAndIdentityPersistence(t *testing.T) {
	var hits [2]atomic.Int32
	servers := make([]*httptest.Server, 2)
	for i := range servers {
		index := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[index].Add(1)
			fmt.Fprint(w, `{"data":[{"id":"test"}]}`)
		}))
		defer servers[i].Close()
	}
	home := t.TempDir()
	for i := 0; i < 2; i++ {
		e := parityEntry()
		e.Name = fmt.Sprint("provider", i)
		e.ID = ""
		e.OAuth.AccountUUID = fmt.Sprint("account", i)
		e.BaseURL = "http://localhost:12345"
		e.Transport = &TransportOptions{ProxyMode: "custom", ProxyURL: servers[i].URL}
		if err := Update(home, func(f *File) error { return f.Put(e) }); err != nil {
			t.Fatal(err)
		}
		if _, err := e.ListModels(context.Background(), FixedToken(e.OAuth)); err != nil {
			t.Fatal(err)
		}
	}
	if hits[0].Load() != 1 || hits[1].Load() != 1 {
		t.Fatal("providers used wrong proxy")
	}
	f, _ := Load(home)
	old := f.Get("provider0")
	next := *old
	next.OAuth = mergeToken(old.OAuth, &Token{AccessToken: "rotated"})
	next.Identity = nil
	if err := Update(home, func(f *File) error { return f.Put(&next) }); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(home)
	if f.Get("provider0").Identity.DeviceID != old.Identity.DeviceID || f.Get("provider0").ID != old.ID {
		t.Fatal("rotation reset identity")
	}
	next.OAuth = &Token{AccessToken: "different", AccountUUID: "account1"}
	if err := Update(home, func(f *File) error { return f.Put(&next) }); err == nil {
		t.Fatal("duplicate account accepted on replacement")
	}
	o := TransportOptions{ProxyMode: "custom", ProxyURL: "http://user:password@example.test:8080"}
	if strings.Contains(o.summary(), "password") || strings.Contains(o.summary(), "user") {
		t.Fatal("proxy credential disclosure")
	}
}

func TestTokensConcurrentRejectedAndReplacement(t *testing.T) {
	home := t.TempDir()
	var refreshes atomic.Int32
	oldFlow := oauthFlows[KindAnthropic]
	defer func() { oauthFlows[KindAnthropic] = oldFlow }()
	flow := *oldFlow
	flow.refresh = func(context.Context, *Token) (*Token, error) {
		refreshes.Add(1)
		return &Token{AccessToken: "new", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	oauthFlows[KindAnthropic] = &flow
	e := parityEntry()
	e.OAuth.RefreshToken = "refresh"
	e.OAuth.ExpiresAt = time.Now().Add(time.Hour)
	if err := Update(home, func(f *File) error { return f.Put(e) }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := (&Tokens{Home: home}).For(e.Name).(*tokenSource)
			tok, err := s.RefreshRejected(context.Background(), "access")
			if err != nil || tok.AccessToken != "new" {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refreshed %d times", refreshes.Load())
	}
	flow.refresh = func(context.Context, *Token) (*Token, error) {
		err := Update(home, func(f *File) error {
			f.Get(e.Name).OAuth = &Token{AccessToken: "replacement", AccountUUID: "different"}
			return nil
		})
		return &Token{AccessToken: "stale-result"}, err
	}
	if _, err := (&Tokens{Home: home}).For(e.Name).Token(context.Background(), true); err == nil {
		t.Fatal("stale refresh replaced new login")
	}
	f, _ := Load(home)
	if f.Get(e.Name).OAuth.AccessToken != "replacement" {
		t.Fatal("new login overwritten")
	}
	if err := Update(home, func(f *File) error { f.Get(e.Name).OAuth.ExpiresAt = time.Now().Add(time.Minute); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Tokens{Home: home}).For(e.Name).Token(context.Background(), false); err != nil {
		t.Fatalf("unexpired non-refreshable token: %v", err)
	}
}

func TestProviderLockCancellation(t *testing.T) {
	home := t.TempDir()
	unlock, err := lockProviderFile(context.Background(), home, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := lockProviderFile(ctx, home, "test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
}

func TestInferenceLoginOptionsAndTokenMerge(t *testing.T) {
	l, err := StartLoginWithOptions(KindAnthropic, LoginOptions{Mode: "inference"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.URL, "scope=user%3Ainference") {
		t.Fatal("incorrect inference scope")
	}
	if _, err := StartLoginWithOptions(KindOpenAI, LoginOptions{Mode: "inference"}); err == nil {
		t.Fatal("accepted wrong provider")
	}
	old := &Token{AccountUUID: "account", AccountID: "organization", Scope: "scope", Extra: map[string]string{"old": "a"}}
	next := mergeToken(old, &Token{Extra: map[string]string{"new": "b"}})
	if next.AccountUUID != "account" || next.AccountID != "organization" || next.Extra["old"] != "a" || next.Extra["new"] != "b" {
		t.Fatal("lost token metadata")
	}
	next.Extra["old"] = "changed"
	if old.Extra["old"] != "a" {
		t.Fatal("merge mutated old token")
	}
}
