package portal

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/diag"
)

type problemList struct {
	Problems []ClientProblem `json:"problems"`
	Total    int64           `json:"total"`
	New      int64           `json:"new"`
}

func testReport(id, fp, version string) diag.Report {
	return diag.Report{ID: id, Kind: diag.KindCrash, Process: diag.ProcDaemon, Fingerprint: fp, Version: version, Env: "prod", OS: "darwin", Arch: "arm64",
		OSVersion: "macOS 15.3", OccurredAt: time.Now().Unix(), Message: "panic: runtime error: index out of range [3] with length 3",
		Stack:    "goroutine 7 [running]:\nsharetoken/internal/client.(*Agent).maintain(0x14000123456)\n\tsharetoken/internal/client/agent.go:812 +0x1c4\n",
		Context:  []string{`time=2026-11-24T10:00:00.000+08:00 level=INFO msg="joined the network" component=daemon user=…`},
		Features: map[string]string{"share": "on", "tailnet": "off"}}
}

func TestClientReports(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	a.hdr = map[string]string{ClientVersionHeader: "1.0.5", ClientPlatformHeader: "darwin/arm64"}
	_, alice := testUser(t, s, "alice@example.com")
	_, bob := testUser(t, s, "bob@example.com")
	const fp = "0123456789abcdef"

	// It takes a login, and the report is checked.
	a.call("POST", "/api/v1/client-reports", "", testReport("aaaaaaaaaaaa", fp, "1.0.5"), 401, nil)
	for _, bad := range []func(r *diag.Report){
		func(r *diag.Report) { r.ID = "x" },
		func(r *diag.Report) { r.Kind = "error" },
		func(r *diag.Report) { r.Process = "other" },
		func(r *diag.Report) { r.Fingerprint = "nothex!!nothex!!" },
		func(r *diag.Report) { r.Version = "<script>" },
		func(r *diag.Report) { r.Message = "" },
		func(r *diag.Report) { r.OccurredAt = 0 },
		func(r *diag.Report) { r.Features = map[string]string{"Bad Key": "on"} },
	} {
		r := testReport("bbbbbbbbbbbb", fp, "1.0.5")
		bad(&r)
		a.call("POST", "/api/v1/client-reports", alice, r, 400, nil)
	}

	// The portal redacts what the client left: an e-mail address, a token.
	leaky := testReport("cccccccccccc", fp, "1.0.5")
	leaky.Message = "panic: sign-in for carol@example.org failed: Bearer st_00112233445566778899aabbccddeeff"
	leaky.Context = append(leaky.Context, "an untidy line from 203.0.113.9 with sk-abcdefghijklmnopqrstu")
	a.call("POST", "/api/v1/client-reports", alice, leaky, 201, nil)
	// The same report again (its answer was lost) does not count twice.
	a.call("POST", "/api/v1/client-reports", alice, leaky, 201, nil)
	a.call("POST", "/api/v1/client-reports", alice, testReport("dddddddddddd", fp, "1.0.5"), 201, nil)
	a.call("POST", "/api/v1/client-reports", bob, testReport("eeeeeeeeeeee", fp, "1.0.4"), 201, nil)
	other := testReport("ffffffffffff", "fedcba9876543210", "1.0.5")
	other.Kind, other.Process = diag.KindPanic, diag.ProcGUI
	a.call("POST", "/api/v1/client-reports", bob, other, 201, nil)

	// Only admins see them.
	a.call("GET", "/api/v1/admin/client-reports", alice, nil, 403, nil)
	root, err := s.store.CreateUser("root@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.store.IssueSessionToken(root.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var list problemList
	a.call("GET", "/api/v1/admin/client-reports?status=", admin, nil, 200, &list)
	if list.Total != 2 || list.New != 2 || len(list.Problems) != 2 {
		t.Fatalf("problems: %+v", list)
	}
	p := list.Problems[0]
	if p.Fingerprint != fp {
		p = list.Problems[1]
	}
	if p.Fingerprint != fp || p.Count != 3 || p.Users != 2 || strings.Join(p.Versions, ",") != "1.0.4,1.0.5" || p.Kind != "crash" || p.Status != "new" {
		t.Fatalf("problem: %+v", p)
	}

	var d ClientProblemDetail
	a.call("GET", "/api/v1/admin/client-reports/"+fp, admin, nil, 200, &d)
	if len(d.Rows) != 2 || d.Rows[0].Features["share"] != "on" {
		t.Fatalf("detail rows: %+v", d.Rows)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT CONCAT(message, context) FROM client_reports WHERE last_report_id = 'cccccccccccc' OR (fingerprint = ? AND user_id = (SELECT id FROM users WHERE username = 'alice@example.com'))`, fp).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"carol@example.org", "st_0011", "203.0.113.9", "sk-abcdef"} {
		if strings.Contains(stored, leak) {
			t.Fatalf("stored %q: %s", leak, stored)
		}
	}
	a.call("GET", "/api/v1/admin/client-reports/0000000000000000", admin, nil, 404, nil)
	a.call("GET", "/api/v1/admin/client-reports/nope", admin, nil, 400, nil)

	// Searching, and the states.
	a.call("GET", "/api/v1/admin/client-reports?status=&q=1.0.4", admin, nil, 200, &list)
	if list.Total != 1 || list.Problems[0].Fingerprint != fp {
		t.Fatalf("search by version: %+v", list)
	}
	a.call("POST", "/api/v1/admin/client-reports/"+fp+"/status", admin, map[string]string{"status": "fixed", "note": "fixed in 1.0.7"}, 200, nil)
	a.call("POST", "/api/v1/admin/client-reports/"+fp+"/status", admin, map[string]string{"status": "bogus"}, 400, nil)
	a.call("POST", "/api/v1/admin/client-reports/0000000000000000/status", admin, map[string]string{"status": "known"}, 404, nil)
	a.call("GET", "/api/v1/admin/client-reports?status=fixed", admin, nil, 200, &list)
	if list.Total != 1 || list.Problems[0].Note != "fixed in 1.0.7" || list.Problems[0].HandledBy != "root@example.com" || list.New != 1 {
		t.Fatalf("fixed: %+v", list)
	}
	// A fixed problem that comes back is open again.
	a.call("POST", "/api/v1/client-reports", bob, testReport("abababababab", fp, "1.0.4"), 201, nil)
	a.call("GET", "/api/v1/admin/client-reports?status=new", admin, nil, 200, &list)
	if list.Total != 2 {
		t.Fatalf("reopened: %+v", list)
	}

	// The dashboard counts the last 7 days.
	dash, err := s.Dashboard()
	if err != nil {
		t.Fatal(err)
	}
	if dash.Crashes.Problems != 2 || dash.Crashes.Users != 2 || dash.Crashes.Reports != 5 || len(dash.Crashes.ByVersion) != 2 {
		t.Fatalf("dashboard: %+v", dash.Crashes)
	}

	// The export.
	a.call("GET", "/api/v1/admin/client-reports?status=&format=csv", admin, nil, 200, nil)

	// Rows nobody reported for 90 days go.
	clock = clock.Add(ClientReportRetention + time.Hour)
	if n, err := s.pruneClientReports(); err != nil || n != 3 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

func TestClientReportLimits(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	a.hdr = map[string]string{ClientVersionHeader: "1.0.5"}
	_, alice := testUser(t, s, "alice@example.com")
	for i := range ClientReportsPerUserHour {
		a.call("POST", "/api/v1/client-reports", alice, testReport(fmt.Sprintf("%012x", i+1), "0123456789abcdef", "1.0.5"), 201, nil)
	}
	a.call("POST", "/api/v1/client-reports", alice, testReport("ffffffffffff", "0123456789abcdef", "1.0.5"), 429, nil)
	clock = clock.Add(time.Hour + time.Second)
	a.call("POST", "/api/v1/client-reports", alice, testReport("fffffffffffe", "0123456789abcdef", "1.0.5"), 201, nil)

	// A report at the size limit fits in a request.
	big := testReport("fffffffffffd", "0123456789abcdef", "1.0.5")
	big.Stack = strings.Repeat("x", diag.MaxStack)
	big.Message = strings.Repeat("m", diag.MaxMessage)
	for range diag.MaxContext {
		big.Context = append(big.Context, strings.Repeat("c", diag.MaxContextLine))
	}
	a.call("POST", "/api/v1/client-reports", alice, big, 201, nil)

	// Everybody together: an hourly count for the site, and a table that fills up.
	clock = clock.Add(2 * time.Hour)
	for range ClientReportsPerHour {
		s.reportsByIP.take(allReports, ClientReportsPerHour, clock)
	}
	a.call("POST", "/api/v1/client-reports", alice, testReport("fffffffffffc", "0123456789abcdef", "1.0.5"), 429, nil)
	clock = clock.Add(2 * time.Hour)
	defer func(n int) { maxClientReports = n }(maxClientReports)
	maxClientReports = 1 // what the table holds: one problem, reported many times
	a.call("POST", "/api/v1/client-reports", alice, testReport("fffffffffffb", "0123456789abcdef", "1.0.5"), 429, nil)
	maxClientReports++
	a.call("POST", "/api/v1/client-reports", alice, testReport("fffffffffffb", "0123456789abcdef", "1.0.5"), 201, nil)
}
