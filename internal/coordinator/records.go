package coordinator

import (
	"database/sql"
	"math"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/ledger"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/version"
)

// Audit verdicts.
const (
	AuditMatch        = "match"        // the node's output agrees with the reference model
	AuditMismatch     = "mismatch"     // it does not: wrong model, or tampered output/usage
	AuditInconclusive = "inconclusive" // too close to call; no effect on the node
)

// AuditRecord is the result of one audit: one or more questions put to a
// node, each a job of its own (JobID is the first).
type AuditRecord struct {
	JobID      string  `json:"job_id"`
	Node       string  `json:"node"`
	Model      string  `json:"model"`
	Verdict    string  `json:"verdict"`
	Similarity float64 `json:"similarity"` // legacy LCS audits; 0 on token-level ones
	Detail     string  `json:"detail"`
	At         int64   `json:"at"`
	// Token-level audits (package audit).
	Method    string  `json:"method,omitempty"`    // tf, div, none; "" on legacy records
	Questions int     `json:"questions,omitempty"` // questions asked
	Score     float64 `json:"score"`               // tf: mean top-1 rate; div: failed divergences
	Threshold float64 `json:"threshold"`           // tf: mean below which the model differs
	// Shadow: recorded only. The verdict did not touch the node's record.
	Shadow bool `json:"shadow"`
	// Scores holds each question's score as JSON, for inspection.
	Scores string `json:"scores,omitempty"`
}

// AuditCalibration is the score distribution of honest answers for one
// reference model: the reference scoring its own answers, which is what an
// honest node returns. Token-level audits take their threshold from it.
type AuditCalibration struct {
	At           int64   `json:"at"`
	Model        string  `json:"model"`
	Method       string  `json:"method"`
	Samples      int     `json:"samples"`       // questions answered and scored
	HonestMean   float64 `json:"honest_mean"`   // tf: mean top-1 rate per question
	HonestMedian float64 `json:"honest_median"` // tf: median of K-question means
	HonestP001   float64 `json:"honest_p001"`   // tf: 0.1th percentile of K-question means
	FailRate     float64 `json:"fail_rate"`     // div: share of honest answers failing the check
	Threshold    float64 `json:"threshold"`     // tf: what audits use
	// Drift: the honest distribution moved since the previous calibration
	// (the provider changed the model behind the name). Audits of the model
	// are recorded without penalty until a calibration without drift.
	Drift  bool   `json:"drift"`
	Detail string `json:"detail"`
}

// SaveCalibration stores a calibration run.
func (e *Engine) SaveCalibration(c AuditCalibration) error {
	if c.At == 0 {
		c.At = e.cfg.Now().UnixNano()
	}
	return e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit_calibrations (at, model, method, samples, honest_mean, honest_median, honest_p001, fail_rate, threshold, drift, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.At, c.Model, c.Method, c.Samples, c.HonestMean, c.HonestMedian, c.HonestP001, c.FailRate, c.Threshold, c.Drift, c.Detail)
		return err
	})
}

// LatestCalibration returns the newest calibration of model, or nil.
func (e *Engine) LatestCalibration(model string) (*AuditCalibration, error) {
	cs, err := QueryCalibrations(e.db, model, 1)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// AuditorLifetime is how long an audit identity lives: after it, once none of
// its jobs is open, its leftover credits return to the platform and it can no
// longer buy. An audit asks several questions, spread out and scored in
// between, all under one identity.
const AuditorLifetime = time.Hour

// RegisterAuditor records peer as an audit identity and funds it out of the
// platform's fees. Its leftover credits return to the platform once its jobs
// are settled (Tick).
func (e *Engine) RegisterAuditor(peer string, micros uint64) error {
	if micros == 0 || micros > math.MaxInt64 {
		return ledger.ErrInvalidAmount
	}
	now := e.cfg.Now()
	return e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		if err := upsert(tx, `UPDATE peers SET auditor = 1 WHERE peer = ?`, []any{peer},
			`INSERT INTO peers (peer, auditor, created_at) VALUES (?, 1, ?)`, []any{peer, now.UnixNano()}); err != nil {
			return err
		}
		return led.Fund(ledger.Account(peer), int64(micros), "audit funding")
	})
}

// RecordAudit stores an audit result and updates the node's record. Repeated
// mismatches suspend the node from matching for Config.SuspendFor.
func (e *Engine) RecordAudit(rec AuditRecord) (*NodeStats, error) {
	now := e.cfg.Now()
	rec.At = now.UnixNano()
	var st *NodeStats
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) (err error) {
		if _, err := tx.Exec(`INSERT INTO audits (at, job_id, node, model, verdict, similarity, detail, method, questions, score, threshold, shadow, scores)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.At, rec.JobID, rec.Node, rec.Model, rec.Verdict, rec.Similarity, rec.Detail,
			rec.Method, rec.Questions, rec.Score, rec.Threshold, rec.Shadow, rec.Scores); err != nil {
			return err
		}
		if rec.Shadow {
			st, err = loadStats(tx, rec.Node)
			return err
		}
		st, err = updateStats(tx, rec.Node, func(s *NodeStats) {
			switch rec.Verdict {
			case AuditMatch:
				s.AuditsPassed++
				s.AuditFailStreak = 0
				s.Reputation = min(maxReputation, s.Reputation+repPerAuditPass)
			case AuditMismatch:
				s.AuditsFailed++
				s.AuditFailStreak++
				s.Reputation = max(0, s.Reputation-repPerAuditFail)
				if s.AuditFailStreak >= auditFailsSuspend {
					s.SuspendedUntil = now.Add(e.cfg.SuspendFor).UnixNano()
				}
			}
		})
		return err
	})
	return st, err
}

// NodeStats returns a node's record.
func (e *Engine) NodeStats(node string) (st *NodeStats, err error) {
	err = e.update(func(tx *sql.Tx, _ *ledger.Tx) error { st, err = loadStats(tx, node); return err })
	return st, err
}

// Nodes lists every node with a record, best reputation first.
func (e *Engine) Nodes() (out []NodeStats, err error) {
	err = e.update(func(tx *sql.Tx, _ *ledger.Tx) error { out, err = QueryNodes(tx); return err })
	return out, err
}

// Audits lists the most recent audit results.
func (e *Engine) Audits(limit int) (out []AuditRecord, err error) {
	err = e.update(func(tx *sql.Tx, _ *ledger.Tx) error { out, err = QueryAudits(tx, limit); return err })
	return out, err
}

// DisputeInfo describes a disputed or judged job for operators.
type DisputeInfo struct {
	JobID     string `json:"job_id"`
	Buyer     string `json:"buyer"`
	Node      string `json:"node"`
	BuyerAcct string `json:"buyer_acct,omitempty"`
	NodeAcct  string `json:"node_acct,omitempty"`
	Model     string `json:"model"`
	Status    Status `json:"status"`
	Cost      uint64 `json:"cost"`
	Reason    string `json:"reason"`
	Outcome   string `json:"outcome,omitempty"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt int64  `json:"created_at"`
	DoneAt    int64  `json:"done_at,omitempty"`
}

// Disputes lists jobs awaiting arbitration first, then judged ones, newest first.
func (e *Engine) Disputes(limit int) (out []DisputeInfo, err error) {
	err = e.update(func(tx *sql.Tx, _ *ledger.Tx) error { out, err = QueryDisputes(tx, limit); return err })
	return out, err
}

// LiveOffer is an offer plus what the coordinator verified about it.
type LiveOffer struct {
	protocol.Offer
	AttestedImage string `json:"attested_image,omitempty"`
}

// LiveOffers returns the live offers with their attestation status.
func (e *Engine) LiveOffers() []LiveOffer {
	now := e.cfg.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []LiveOffer
	for _, oe := range e.offers {
		if oe.offer.ExpiresAt > now {
			out = append(out, LiveOffer{Offer: oe.offer, AttestedImage: oe.tee})
		}
	}
	return out
}

// registeredRelays lists the enabled relays in the database.
func (e *Engine) registeredRelays() ([]accounts.Relay, error) {
	var out []accounts.Relay
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) (err error) {
		out, err = accounts.Relays(tx, true)
		return err
	})
	return out, err
}

// recordRelayStatus writes whether the coordinator is reachable through each
// registered relay; the portal hands users only relays that work.
func (e *Engine) recordRelayStatus(st []p2p.RelayStatus) error {
	now := e.cfg.Now()
	return e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		for _, r := range st {
			if err := accounts.SetRelayStatus(tx, r.Addr, r.Reserved, r.Err, r.RTT.Milliseconds(), relayRelease(r.Agent), now); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunningVersion reads the version of the coordinator serving this database.
func RunningVersion(q Querier) (string, error) {
	var v string
	err := q.QueryRow("SELECT value FROM meta WHERE `key` = 'coordinator_version'").Scan(&v)
	return v, err
}

// relayRelease is the release a relay announces in its identify agent
// ("sharetoken-relay/1.0.0 (linux/amd64)" is 1.0.0); "" for anything else.
func relayRelease(agent string) string {
	if a, ok := version.ParseAgent(agent); ok && a.Program == "relay" {
		return a.Version
	}
	return ""
}
