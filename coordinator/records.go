package coordinator

import (
	"database/sql"
	"math"
	"strings"

	"sharetoken/accounts"
	"sharetoken/ledger"
	"sharetoken/p2p"
	"sharetoken/protocol"
)

// Audit verdicts.
const (
	AuditMatch        = "match"        // the node's output agrees with the reference model
	AuditMismatch     = "mismatch"     // it does not: wrong model, or tampered output/usage
	AuditInconclusive = "inconclusive" // too close to call; no effect on the node
)

// AuditRecord is the result of one audit job.
type AuditRecord struct {
	JobID      string  `json:"job_id"`
	Node       string  `json:"node"`
	Model      string  `json:"model"`
	Verdict    string  `json:"verdict"`
	Similarity float64 `json:"similarity"`
	Detail     string  `json:"detail"`
	At         int64   `json:"at"`
}

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
		if _, err := tx.Exec(`INSERT INTO audits (at, job_id, node, model, verdict, similarity, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			rec.At, rec.JobID, rec.Node, rec.Model, rec.Verdict, rec.Similarity, rec.Detail); err != nil {
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
			if err := accounts.SetRelayStatus(tx, r.Addr, r.Reserved, r.Err, r.RTT.Milliseconds(), strings.TrimPrefix(r.Agent, "sharetoken-relay/"), now); err != nil {
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
