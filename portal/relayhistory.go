package portal

import (
	"net/http"
	"sort"
	"time"
)

// Relay metric history: every successful scrape is kept for a week so the
// admin console can chart CPU, memory and network per relay.

const relaySampleKeep = 8 * 24 * time.Hour

// saveRelaySample stores one scrape; old samples are pruned about hourly.
func (s *Server) saveRelaySample(m RelayMetrics) {
	if _, err := s.db.Exec(`INSERT INTO relay_samples (relay, at, cpu, rss, rx_bps, tx_bps) VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE cpu = VALUES(cpu), rss = VALUES(rss), rx_bps = VALUES(rx_bps), tx_bps = VALUES(tx_bps)`,
		m.Relay, m.OKAt, m.CPUPercent, m.RSSBytes, m.NetRxBps, m.NetTxBps); err != nil {
		s.log.Warn("saving a relay sample", "relay", m.Relay, "err", err)
		return
	}
	if time.Duration(m.OKAt)%time.Hour < s.relayMetrics.cfg.Interval {
		if _, err := s.db.Exec(`DELETE FROM relay_samples WHERE at < ?`, m.OKAt-int64(relaySampleKeep)); err != nil {
			s.log.Warn("pruning relay samples", "err", err)
		}
	}
}

// RelayPoint is one averaged bucket of a relay's samples.
type RelayPoint struct {
	At    int64   `json:"at"` // unix nanos, start of the bucket
	CPU   float64 `json:"cpu_percent"`
	RSS   float64 `json:"rss_bytes"`
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
}

type RelaySeries struct {
	Relay  string       `json:"relay"`
	Points []RelayPoint `json:"points"`
}

type RelayHistory struct {
	Now    int64         `json:"now"`
	From   int64         `json:"from"`
	Bucket int64         `json:"bucket_ns"`
	Series []RelaySeries `json:"series"`
}

// historyRanges maps the console's range choices to a window and a bucket.
var historyRanges = map[string][2]time.Duration{
	"24h": {24 * time.Hour, 5 * time.Minute},
	"3d":  {72 * time.Hour, 15 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
}

func (s *Server) handleRelayHistory(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	rg, ok := historyRanges[r.URL.Query().Get("range")]
	if !ok {
		rg = historyRanges["24h"]
	}
	now := s.cfg.Now()
	from, bucket := now.Add(-rg[0]).UnixNano(), int64(rg[1])
	// Averages skip the -1 "unknown" rates; a bucket with none reports -1.
	rows, err := s.db.Query(`SELECT relay, FLOOR(at / ?) b,
		COALESCE(AVG(CASE WHEN cpu >= 0 THEN cpu END), -1), AVG(rss),
		COALESCE(AVG(CASE WHEN rx_bps >= 0 THEN rx_bps END), -1), COALESCE(AVG(CASE WHEN tx_bps >= 0 THEN tx_bps END), -1)
		FROM relay_samples WHERE at >= ? GROUP BY relay, b ORDER BY relay, b`, bucket, from)
	if err != nil {
		s.internal(w, err)
		return
	}
	defer rows.Close()
	by := map[string]*RelaySeries{}
	for rows.Next() {
		var name string
		var b int64
		var p RelayPoint
		if err := rows.Scan(&name, &b, &p.CPU, &p.RSS, &p.RxBps, &p.TxBps); err != nil {
			s.internal(w, err)
			return
		}
		p.At = b * bucket
		if by[name] == nil {
			by[name] = &RelaySeries{Relay: name}
		}
		by[name].Points = append(by[name].Points, p)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, err)
		return
	}
	h := RelayHistory{Now: now.UnixNano(), From: from, Bucket: bucket, Series: []RelaySeries{}}
	for _, sr := range by {
		h.Series = append(h.Series, *sr)
	}
	sort.Slice(h.Series, func(i, j int) bool { return h.Series[i].Relay < h.Series[j].Relay })
	writeJSON(w, http.StatusOK, h)
}
