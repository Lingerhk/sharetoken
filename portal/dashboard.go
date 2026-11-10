package portal

import (
	"net/http"
	"time"

	"sharetoken/accounts"
	"sharetoken/coordinator"
)

// Dashboard is the admin console's live picture of the platform: people,
// devices, traffic and every relay, with the relays' own metrics when the
// portal reads them over SSH (relaymetrics.go).
type Dashboard struct {
	Now          int64            `json:"now"`
	Coordinator  string           `json:"coordinator,omitempty"`
	Users        DashUsers        `json:"users"`
	Devices      DashDevices      `json:"devices"`
	Nodes        DashNodes        `json:"nodes"`
	Jobs         map[string]int64 `json:"jobs"`  // all time, by status
	Day          DashWindow       `json:"day"`   // the last 24 hours
	Daily        []DashDay        `json:"daily"` // the last 14 local days, oldest first
	Versions     []DashVersion    `json:"client_versions"`
	Relays       []DashRelay      `json:"relays"`
	MetricsOn    bool             `json:"relay_metrics"` // the portal reads relay metrics
	MetricsEvery int64            `json:"relay_metrics_interval_s,omitempty"`
}

type DashUsers struct {
	Total     int64 `json:"total"`
	Admins    int64 `json:"admins"`
	Disabled  int64 `json:"disabled"`
	New24h    int64 `json:"new_24h"`
	New7d     int64 `json:"new_7d"`
	Active24h int64 `json:"active_24h"` // used a token or a device in the last 24 hours
}

type DashDevices struct {
	Total   int64 `json:"total"`
	Online  int64 `json:"online"` // seen in the last 10 minutes (the coordinator records at most every 5)
	Seen24h int64 `json:"seen_24h"`
}

type DashNodes struct {
	Total     int64 `json:"total"`
	Suspended int64 `json:"suspended"`
	Active24h int64 `json:"active_24h"` // served a job in the last 24 hours
}

type DashWindow struct {
	Jobs     int64            `json:"jobs"`
	ByStatus map[string]int64 `json:"by_status"`
	Cost     int64            `json:"cost_micros"`
	Fees     int64            `json:"fee_micros"`
	Tokens   int64            `json:"tokens"`
	Buyers   int64            `json:"buyers"`
}

type DashDay struct {
	Day    string `json:"day"` // YYYY-MM-DD, server-local
	Users  int64  `json:"users"`
	Jobs   int64  `json:"jobs"`
	Cost   int64  `json:"cost_micros"`
	Tokens int64  `json:"tokens"`
}

type DashVersion struct {
	Version string `json:"version"`
	Devices int64  `json:"devices"`
}

type DashRelay struct {
	accounts.Relay
	Metrics *RelayMetrics `json:"metrics,omitempty"`
}

const dashDays = 14

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	d, err := s.Dashboard()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// Dashboard gathers the dashboard. Reads only, outside any transaction.
func (s *Server) Dashboard() (*Dashboard, error) {
	now := s.cfg.Now()
	ns := now.UnixNano()
	dayAgo, weekAgo := now.Add(-24*time.Hour).UnixNano(), now.Add(-7*24*time.Hour).UnixNano()
	d := &Dashboard{Now: ns, Jobs: map[string]int64{}, Day: DashWindow{ByStatus: map[string]int64{}}}
	q := func(dst []any, query string, args ...any) error { return s.db.QueryRow(query, args...).Scan(dst...) }

	if err := q([]any{&d.Users.Total, &d.Users.Admins, &d.Users.Disabled, &d.Users.New24h, &d.Users.New7d},
		`SELECT COUNT(*), COALESCE(SUM(is_admin <> 0), 0), COALESCE(SUM(disabled <> 0), 0),
		        COALESCE(SUM(created_at >= ?), 0), COALESCE(SUM(created_at >= ?), 0) FROM users`, dayAgo, weekAgo); err != nil {
		return nil, err
	}
	if err := q([]any{&d.Users.Active24h},
		`SELECT COUNT(*) FROM (SELECT user_id FROM api_tokens WHERE last_used_at >= ?
		 UNION SELECT user_id FROM devices WHERE last_seen_at >= ?) a`, dayAgo, dayAgo); err != nil {
		return nil, err
	}
	if err := q([]any{&d.Devices.Total, &d.Devices.Online, &d.Devices.Seen24h},
		`SELECT COUNT(*), COALESCE(SUM(last_seen_at >= ?), 0), COALESCE(SUM(last_seen_at >= ?), 0) FROM devices`,
		now.Add(-10*time.Minute).UnixNano(), dayAgo); err != nil {
		return nil, err
	}
	if err := q([]any{&d.Nodes.Total, &d.Nodes.Suspended},
		`SELECT COUNT(*), COALESCE(SUM(suspended_until > ?), 0) FROM nodes`, ns); err != nil {
		return nil, err
	}
	counts, err := coordinator.JobCounts(s.db)
	if err != nil {
		return nil, err
	}
	for st, n := range counts {
		d.Jobs[string(st)] = n
	}

	// The last 24 hours, through the created_at index.
	rows, err := s.db.Query(`SELECT status, COUNT(*), COALESCE(SUM(cost), 0), COALESCE(SUM(fee), 0), COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		FROM jobs WHERE created_at >= ? GROUP BY status`, dayAgo)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n, cost, fee, tok int64
		if err := rows.Scan(&st, &n, &cost, &fee, &tok); err != nil {
			rows.Close()
			return nil, err
		}
		d.Day.ByStatus[st] = n
		d.Day.Jobs += n
		d.Day.Cost += cost
		d.Day.Fees += fee
		d.Day.Tokens += tok
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := q([]any{&d.Day.Buyers, &d.Nodes.Active24h},
		`SELECT COUNT(DISTINCT buyer_acct), COUNT(DISTINCT node) FROM jobs WHERE created_at >= ?`, dayAgo); err != nil {
		return nil, err
	}

	// Daily series: local days, the oldest first.
	y, m, dd := now.Date()
	first := time.Date(y, m, dd, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(dashDays - 1))
	d.Daily = make([]DashDay, dashDays)
	starts := make([]int64, dashDays+1)
	for i := range dashDays + 1 {
		t := first.AddDate(0, 0, i)
		starts[i] = t.UnixNano()
		if i < dashDays {
			d.Daily[i].Day = t.Format("2006-01-02")
		}
	}
	bucket := func(at int64) int { // days can be 23 or 25 hours long
		for i := dashDays - 1; i >= 0; i-- {
			if at >= starts[i] {
				return i
			}
		}
		return -1
	}
	// Grouped by hour in SQL, so a busy day costs 24 rows, not one per job.
	hourly, err := s.db.Query(`SELECT FLOOR((created_at - ?) / 3600000000000) h, COUNT(*), COALESCE(SUM(cost), 0), COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		FROM jobs WHERE created_at >= ? GROUP BY h`, starts[0], starts[0])
	if err != nil {
		return nil, err
	}
	for hourly.Next() {
		var h, n, cost, tok int64
		if err := hourly.Scan(&h, &n, &cost, &tok); err != nil {
			hourly.Close()
			return nil, err
		}
		if i := bucket(starts[0] + h*int64(time.Hour)); i >= 0 {
			d.Daily[i].Jobs += n
			d.Daily[i].Cost += cost
			d.Daily[i].Tokens += tok
		}
	}
	hourly.Close()
	if err := hourly.Err(); err != nil {
		return nil, err
	}
	signups, err := s.db.Query(`SELECT created_at FROM users WHERE created_at >= ?`, starts[0])
	if err != nil {
		return nil, err
	}
	for signups.Next() {
		var at int64
		if err := signups.Scan(&at); err != nil {
			signups.Close()
			return nil, err
		}
		if i := bucket(at); i >= 0 {
			d.Daily[i].Users++
		}
	}
	signups.Close()
	if err := signups.Err(); err != nil {
		return nil, err
	}

	vrows, err := s.db.Query(`SELECT client_version, COUNT(*) FROM devices GROUP BY client_version ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	for vrows.Next() {
		var v DashVersion
		if err := vrows.Scan(&v.Version, &v.Devices); err != nil {
			vrows.Close()
			return nil, err
		}
		d.Versions = append(d.Versions, v)
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, err
	}

	if d.Coordinator, err = coordinator.CoordinatorID(s.db); err != nil {
		return nil, err
	}
	if d.Relays, err = s.relaysWithMetrics(); err != nil {
		return nil, err
	}
	d.MetricsOn = s.relayMetrics != nil
	if d.MetricsOn {
		d.MetricsEvery = int64(s.relayMetrics.cfg.Interval / time.Second)
	}
	return d, nil
}

// relaysWithMetrics is the relay registry with each relay's last scrape.
func (s *Server) relaysWithMetrics() ([]DashRelay, error) {
	relays, err := s.store.Relays(false)
	if err != nil {
		return nil, err
	}
	snap := s.relayMetrics.snapshot()
	out := make([]DashRelay, 0, len(relays))
	for _, rl := range relays {
		dr := DashRelay{Relay: rl}
		if m, ok := snap[rl.Name]; ok {
			dr.Metrics = &m
		}
		out = append(out, dr)
	}
	return out, nil
}
