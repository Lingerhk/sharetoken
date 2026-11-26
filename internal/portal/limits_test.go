package portal

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/totp"
)

// A full table drops only keys whose failures have aged out, and takes no new
// key while every one still counts: filling it resets nobody.
func TestFailTableFullKeepsLiveKeys(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var warned atomic.Int64
	done := make(chan struct{}, 8)
	tb := failTable{max: 100, onFull: func(int) { warned.Add(1); done <- struct{}{} }}
	for range maxFails {
		tb.add("victim", failWindow, now)
	}
	tb.add("old", failWindow, now.Add(-failWindow-time.Second)) // aged out by now
	for i := 0; tb.keys() < 100; i++ {
		tb.add(fmt.Sprintf("flood-%d", i), failWindow, now)
	}
	// Full of live keys, bar one: the next key takes the aged one's place.
	now = now.Add(failSweepEvery)
	tb.add("late", failWindow, now)
	if tb.count("late", failWindow, now) != 1 || tb.count("old", failWindow, now) != 0 || tb.keys() != 100 {
		t.Fatalf("an aged key did not make room: %d keys", tb.keys())
	}
	// Now nothing can go: new keys pass uncounted, old ones keep counting.
	now = now.Add(failSweepEvery)
	for i := range 500 {
		if !tb.take(fmt.Sprintf("more-%d", i), failWindow, 1, now) {
			t.Fatal("a key without room was refused")
		}
	}
	<-done
	if tb.keys() != 100 || tb.count("victim", failWindow, now) != maxFails || tb.count("more-1", failWindow, now) != 0 {
		t.Fatalf("a full table changed: %d keys, victim %d", tb.keys(), tb.count("victim", failWindow, now))
	}
	if tb.take("victim", failWindow, maxFails, now) {
		t.Fatal("a counter in a full table stopped counting")
	}
	if warned.Load() != 1 {
		t.Fatalf("%d warnings for one full table", warned.Load())
	}
	// Once the flood ages out there is room again.
	now = now.Add(failWindow + time.Second)
	if tb.add("fresh", failWindow, now); tb.count("fresh", failWindow, now) != 1 || tb.keys() != 1 {
		t.Fatalf("%d keys after everything aged out", tb.keys())
	}
}

func TestFailTableTakeAndGiveBack(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var tb failTable
	var wg sync.WaitGroup
	var got atomic.Int64
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tb.take("k", time.Minute, 7, now) {
				got.Add(1)
			}
		}()
	}
	wg.Wait()
	if got.Load() != 7 || tb.count("k", time.Minute, now) != 7 {
		t.Fatalf("%d taken, %d counted, want 7", got.Load(), tb.count("k", time.Minute, now))
	}
	tb.giveBack("k", now)
	if tb.count("k", time.Minute, now) != 6 || !tb.take("k", time.Minute, 7, now) {
		t.Fatal("a place given back was not free again")
	}
	if tb.count("k", time.Minute, now.Add(2*time.Minute)) != 0 || tb.keys() != 0 {
		t.Fatal("failures did not age out")
	}
}

// The counters over everyone, and those per account, are out of reach of
// keys made per client address.
func TestSiteCountersSurviveAFlood(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	s := &Server{cfg: Config{Now: func() time.Time { return clock }}}
	s.fails.max = 1000
	for range codeSurgeFails - 1 {
		s.recordFailure(codeFailsKey)
	}
	s.recordFailure("inv:ud:7", "u:alice@example.com")
	for i := range 5000 {
		s.recordFailure(fmt.Sprintf("code:203.0.%d.%d", i/250, i%250))
	}
	if n := s.failCount(codeFailsKey, surgeWindow); n != codeSurgeFails-1 {
		t.Fatalf("%d wrong codes over everyone after a flood of keys, want %d", n, codeSurgeFails-1)
	}
	if s.failCount("inv:ud:7", guessDay) != 1 || s.failCount("u:alice@example.com", failWindow) != 1 {
		t.Fatal("an account's counters were lost in a flood of keys")
	}
	if s.fails.keys() != 1000 {
		t.Fatalf("%d address keys, want the table's 1000", s.fails.keys())
	}
}

func TestRateKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":            "203.0.113.9",
		"::ffff:203.0.113.9":     "203.0.113.9",
		"2001:db8:1:2:3:4:5:6":   "2001:db8:1:2::/64",
		"2001:db8:1:2::1":        "2001:db8:1:2::/64",
		"2001:db8:1:3::1":        "2001:db8:1:3::/64",
		"fe80::1%eth0":           "fe80::/64",
		"not an address":         "not an address",
		"2001:0DB8:0001:0002::9": "2001:db8:1:2::/64",
	} {
		if got := rateKey(in); got != want {
			t.Errorf("rateKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// One challenge tests challengeTries codes, however they are sent.
func TestTOTPGuessesHoldInParallel(t *testing.T) {
	p := newMFAPortal(t)
	const email = "ann@example.com"
	u, err := p.s.CreateUser(email, false)
	if err != nil {
		t.Fatal(err)
	}
	secret := p.enroll(u)
	c := &web{t: t, url: p.url, noWeb: true}
	var ch challengeResp
	c.call("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": p.code(c, email)}, 401, &ch)
	if ch.Error.Code != "mfa_required" || ch.Challenge == "" {
		t.Fatalf("%+v", ch)
	}
	var wg sync.WaitGroup
	var tested atomic.Int64
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, raw := c.do("POST", "/api/v1/mfa", map[string]string{"challenge": ch.Challenge, "code": otherCode(totp.Code(secret, p.now))})
			switch code := errCode(t, raw); {
			case resp.StatusCode == 401 && code == "bad_code":
				tested.Add(1)
			case code != "challenge_expired" && code != "throttled":
				t.Errorf("%d %s", resp.StatusCode, raw)
			}
		}()
	}
	wg.Wait()
	if n := tested.Load(); n != challengeTries {
		t.Fatalf("%d codes tested against one challenge in parallel, want %d", n, challengeTries)
	}
	// The challenge is gone: the right code is too late.
	c.wantErr("POST", "/api/v1/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 401, "challenge_expired")
}

// The throttle on two-factor codes counts before it checks, with or without
// a challenge.
func TestTOTPThrottleHoldsInParallel(t *testing.T) {
	p := newMFAPortal(t)
	const email = "ann@example.com"
	u, err := p.s.CreateUser(email, false)
	if err != nil {
		t.Fatal(err)
	}
	secret := p.enroll(u)
	tok, _, err := p.s.Store().IssueSessionToken(u.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	c := p.client(tok)
	var wg sync.WaitGroup
	var tested atomic.Int64
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, raw := c.do("POST", "/api/v1/account/mfa/disable", map[string]string{"code": otherCode(totp.Code(secret, p.now))})
			switch {
			case resp.StatusCode == 400:
				tested.Add(1)
			case resp.StatusCode != 429:
				t.Errorf("%d %s", resp.StatusCode, raw)
			}
		}()
	}
	wg.Wait()
	if n := tested.Load(); n != maxFails {
		t.Fatalf("%d codes tested in parallel, want %d", n, maxFails)
	}
}

// Wrong pairing codes and links sent together count one by one: an account
// gets its limit, not as many as it can send before the first answer.
func TestCodeGuessesHoldInParallel(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	_, sellerTok := testUser(t, s, "seller@example.com")
	_, tok := testUser(t, s, "guesser@example.com")
	clock = clock.Add(newAccountAge + time.Hour)
	guess := func(n int, path string, body func(i int) map[string]string) (wrong int) {
		t.Helper()
		var wg sync.WaitGroup
		var tested atomic.Int64
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := &web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + tok}}
				resp, raw := req.do("POST", path, body(i))
				switch {
				case resp.StatusCode == 404:
					tested.Add(1)
				case resp.StatusCode != 429:
					t.Errorf("%d %s", resp.StatusCode, raw)
				}
			}()
		}
		wg.Wait()
		return int(tested.Load())
	}
	if n := guess(40, "/api/v1/pairing/redeem", func(i int) map[string]string { return map[string]string{"code": fmt.Sprintf("AAA-A%02d", i)} }); n != userMaxFails {
		t.Fatalf("%d wrong codes tested in parallel, want %d", n, userMaxFails)
	}
	if n := s.failCount(codeFailsKey, surgeWindow); n != userMaxFails {
		t.Fatalf("%d wrong codes counted over everyone, want %d", n, userMaxFails)
	}
	// The public preview of links: per address.
	clock = clock.Add(guessDay + time.Second)
	var wg sync.WaitGroup
	var tested atomic.Int64
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, raw := (&web{t: t, url: a.url, noWeb: true}).do("GET", fmt.Sprintf("/api/v1/invite/p/AAAAAA%02d", i), nil)
			switch {
			case resp.StatusCode == 404:
				tested.Add(1)
			case resp.StatusCode != 429:
				t.Errorf("%d %s", resp.StatusCode, raw)
			}
		}()
	}
	wg.Wait()
	if n := tested.Load(); n != ipMaxFails {
		t.Fatalf("%d wrong links previewed in parallel, want %d", n, ipMaxFails)
	}

	// A right code is not held against anyone: sent several times at once it
	// works (once: the later ones find it used up), and it leaves no count.
	clock = clock.Add(guessDay + time.Second)
	var code accounts.PairingCode
	a.call("POST", "/api/v1/pairing/codes", sellerTok, map[string]any{"max_uses": 1}, 201, &code)
	var ok atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := &web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + tok}}
			if resp, _ := req.do("POST", "/api/v1/pairing/redeem", map[string]string{"code": code.Code}); resp.StatusCode == 200 {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() == 0 {
		t.Fatal("a right code was refused")
	}
	clock = clock.Add(guessDay + time.Second)
	third, thirdTok := testUser(t, s, "third@example.com")
	a.call("POST", "/api/v1/pairing/codes", sellerTok, map[string]any{}, 201, &code)
	a.call("POST", "/api/v1/pairing/redeem", thirdTok, map[string]string{"code": code.Code}, 200, nil)
	if n := s.failCount(fmt.Sprintf("inv:ud:%d", third.ID), guessDay) + s.failCount(codeFailsKey, surgeWindow) + s.failCount("inv:ipd:127.0.0.1", guessDay); n != 0 {
		t.Fatalf("%d failures counted for a right code", n)
	}
}
