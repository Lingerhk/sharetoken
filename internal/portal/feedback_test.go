package portal

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/accounts"
)

type feedbackList struct {
	Feedback []accounts.Feedback `json:"feedback"`
	New      int64               `json:"new"`
}

func TestFeedback(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	var alice, bob AuthResponse
	_, alice.Token = testUser(t, s, "alice")
	_, bob.Token = testUser(t, s, "bob")

	msg := func(text string) FeedbackRequest {
		return FeedbackRequest{Category: "bug", Message: text, Source: "portal"}
	}

	// It takes a login, and the input is checked.
	a.call("POST", "/api/v1/feedback", "", msg("hi"), 401, nil)
	for _, bad := range []FeedbackRequest{
		msg("   "),
		msg(strings.Repeat("字", accounts.FeedbackMaxRunes+1)),
		{Category: "nope", Message: "x", Source: "portal"},
		{Category: "bug", Message: "x", Source: "nope"},
		{Category: "bug", Message: "x", Source: "client", AppVersion: "<script>"},
		{Category: "bug", Message: "x", Source: "client", OS: strings.Repeat("a", 65)},
		{Category: "bug", Message: "x", Source: "client", Lang: "not a lang"},
	} {
		a.call("POST", "/api/v1/feedback", alice.Token, bad, 400, nil)
	}

	// Text comes back exactly as sent: the pages put it in the DOM as text, and
	// the longest message fits.
	const hostile = `<img src=x onerror=alert(1)> 'quoted' "double" 反馈 🎉`
	a.call("POST", "/api/v1/feedback", alice.Token, msg(hostile), 201, nil)
	a.call("POST", "/api/v1/feedback", alice.Token, msg(strings.Repeat("字", accounts.FeedbackMaxRunes)), 201, nil)
	a.call("POST", "/api/v1/feedback", bob.Token, FeedbackRequest{Category: "idea", Message: "from bob", Source: "client", AppVersion: "1.2.3", OS: "darwin/arm64", Lang: "zh-TW"}, 201, nil)

	// A user sees only their own.
	var mine feedbackList
	a.call("GET", "/api/v1/feedback", alice.Token, nil, 200, &mine)
	if len(mine.Feedback) != 2 || mine.Feedback[1].Message != hostile || mine.Feedback[0].Status != accounts.FeedbackNew {
		t.Fatalf("alice's feedback: %+v", mine.Feedback)
	}
	a.call("GET", "/api/v1/feedback?limit=1&page=2", alice.Token, nil, 200, &mine)
	if len(mine.Feedback) != 1 || mine.Feedback[0].Message != hostile {
		t.Fatalf("paging: %+v", mine.Feedback)
	}

	// Only admins reach the admin endpoints.
	a.call("GET", "/api/v1/admin/feedback", alice.Token, nil, 403, nil)
	a.call("POST", "/api/v1/admin/feedback/1/status", alice.Token, map[string]string{"status": "done"}, 403, nil)
	root, err := s.store.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.store.IssueSessionToken(root.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}

	var all feedbackList
	a.call("GET", "/api/v1/admin/feedback", admin, nil, 200, &all)
	if len(all.Feedback) != 3 || all.New != 3 {
		t.Fatalf("admin list: %+v", all)
	}
	got := all.Feedback[0]
	if got.Username != "bob" || got.Source != "client" || got.AppVersion != "1.2.3" || got.OS != "darwin/arm64" || got.Lang != "zh-TW" {
		t.Fatalf("admin sees: %+v", got)
	}

	// Handling, filtering, reopening.
	path := fmt.Sprintf("/api/v1/admin/feedback/%d/status", got.ID)
	a.call("POST", path, admin, map[string]string{"status": "nope"}, 400, nil)
	a.call("POST", "/api/v1/admin/feedback/99999/status", admin, map[string]string{"status": "done"}, 404, nil)
	a.call("POST", path, admin, map[string]string{"status": "done"}, 200, nil)
	var done feedbackList
	a.call("GET", "/api/v1/admin/feedback?status=done", admin, nil, 200, &done)
	if len(done.Feedback) != 1 || done.New != 2 || done.Feedback[0].HandledBy != "root" || done.Feedback[0].HandledAt == 0 {
		t.Fatalf("done: %+v", done)
	}
	var bobs feedbackList
	a.call("GET", "/api/v1/feedback", bob.Token, nil, 200, &bobs)
	if bobs.Feedback[0].Status != accounts.FeedbackDone || bobs.Feedback[0].HandledBy != "" {
		t.Fatalf("the sender sees the status, not who handled it: %+v", bobs.Feedback[0])
	}
	a.call("POST", path, admin, map[string]string{"status": "new"}, 200, nil)
	var reopened feedbackList
	a.call("GET", "/api/v1/admin/feedback?status=new", admin, nil, 200, &reopened)
	if len(reopened.Feedback) != 3 || reopened.Feedback[0].HandledBy != "" || reopened.Feedback[0].HandledAt != 0 {
		t.Fatalf("reopened: %+v", reopened.Feedback[0])
	}

	// One user's limit does not touch another's.
	for i := 2; i < accounts.FeedbackPerHour; i++ {
		a.call("POST", "/api/v1/feedback", alice.Token, msg("more"), 201, nil)
	}
	a.call("POST", "/api/v1/feedback", alice.Token, msg("too many"), 429, nil)
	a.call("POST", "/api/v1/feedback", bob.Token, msg("still fine"), 201, nil)
}

// Many accounts behind one address share FeedbackPerIPHour; rejected input does
// not count, other addresses are not affected, and the limit lifts after an hour.
func TestFeedbackPerIP(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	msg := FeedbackRequest{Category: "idea", Message: "hi", Source: "portal"}
	from := func(ip string) *api { return &api{t: t, url: a.url, hdr: map[string]string{"X-Real-IP": ip}} }
	one, other := from("203.0.113.7"), from("198.51.100.9")

	n := 0
	for i := 0; n < FeedbackPerIPHour; i++ {
		_, tok := testUser(t, s, fmt.Sprintf("ip%d", i))
		one.call("POST", "/api/v1/feedback", tok, FeedbackRequest{Category: "nope", Message: "x", Source: "portal"}, 400, nil)
		for j := 0; j < accounts.FeedbackPerHour && n < FeedbackPerIPHour; j++ {
			one.call("POST", "/api/v1/feedback", tok, msg, 201, nil)
			n++
		}
	}
	_, fresh := testUser(t, s, "fresh")
	one.call("POST", "/api/v1/feedback", fresh, msg, 429, nil)
	other.call("POST", "/api/v1/feedback", fresh, msg, 201, nil)

	clock = clock.Add(time.Hour + time.Second)
	one.call("POST", "/api/v1/feedback", fresh, msg, 201, nil)
}
