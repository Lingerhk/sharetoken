package coordinator

import (
	"database/sql"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/ledger"
)

// Share partners (package accounts, sharing.go) use each other's nodes for
// free. When a user with partners asks for a model, IssueTicket first looks
// among its partners' nodes and issues a shared ticket (no hold, no charge);
// only when none can take the job does it go to the market, unless the user
// asked for partners only. Each partner bounds what it gives: a token limit
// over the last ShareWindow and a part of each node's slots.

// sharedJobsSchema marks the jobs between partners and indexes what the daily
// limits count.
const sharedJobsSchema = `
ALTER TABLE jobs ADD COLUMN shared BIGINT NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD KEY jobs_shared_by_node (node_acct, shared, created_at);
ALTER TABLE jobs ADD KEY jobs_shared_by_buyer (buyer_acct, shared, created_at);
`

// partnerGrant is what a partner of the buyer gives it right now.
type partnerGrant struct {
	sharePct int64
	// exhausted: the partner's nodes served its partners all the tokens its
	// daily limit allows.
	exhausted bool
}

// readPartners fills sc's partner fields for the user userID.
func readPartners(q interface {
	accounts.Querier
	Query(query string, args ...any) (*sql.Rows, error)
}, sc *buyScope, userID int64, now time.Time) error {
	own, grants, err := accounts.PartnerGrants(q, userID)
	if err != nil {
		return err
	}
	sc.onlyPartners = own.OnlyPartners
	if len(grants) == 0 {
		return nil
	}
	sc.partners = make(map[string]partnerGrant, len(grants))
	for _, g := range grants {
		acct := ledger.UserAccount(g.UserID)
		pg := partnerGrant{sharePct: g.SharePct}
		if g.DailyLimitTokens > 0 {
			used, err := SharedTokensServed(q, acct, now.Add(-accounts.ShareWindow).UnixNano())
			if err != nil {
				return err
			}
			pg.exhausted = used >= g.DailyLimitTokens
		}
		sc.partners[acct] = pg
	}
	return nil
}

// SharedTokensServed is how many tokens (prompt + completion) the nodes of
// acct served its share partners in jobs created at or after since. Jobs still
// running count once they settle, so a limit can be passed by the jobs in
// flight when it is reached.
func SharedTokensServed(q Querier, acct string, since int64) (int64, error) {
	var n int64
	err := q.QueryRow(`SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0) FROM jobs FORCE INDEX (jobs_shared_by_node)
		WHERE node_acct = ? AND shared = 1 AND created_at >= ?`, acct, since).Scan(&n)
	return n, err
}

// ShareFlow is the tokens moved between a user and one partner since a time.
type ShareFlow struct {
	Given int64 `json:"given"` // the user's nodes served the partner
	Taken int64 `json:"taken"` // the partner's nodes served the user
	Jobs  int64 `json:"jobs"`
}

// SharedFlows returns, per partner account, the tokens acct's shared jobs
// moved in jobs created at or after since.
func SharedFlows(q Querier, acct string, since int64) (map[string]ShareFlow, error) {
	out := make(map[string]ShareFlow)
	add := func(query string, given bool) error {
		rows, err := q.Query(query, acct, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var other string
			var tokens, jobs int64
			if err := rows.Scan(&other, &tokens, &jobs); err != nil {
				return err
			}
			f := out[other]
			if given {
				f.Given += tokens
			} else {
				f.Taken += tokens
			}
			f.Jobs += jobs
			out[other] = f
		}
		return rows.Err()
	}
	if err := add(`SELECT buyer_acct, COALESCE(SUM(prompt_tokens + completion_tokens), 0), COUNT(*) FROM jobs FORCE INDEX (jobs_shared_by_node)
		WHERE node_acct = ? AND shared = 1 AND created_at >= ? GROUP BY buyer_acct`, true); err != nil {
		return nil, err
	}
	if err := add(`SELECT node_acct, COALESCE(SUM(prompt_tokens + completion_tokens), 0), COUNT(*) FROM jobs FORCE INDEX (jobs_shared_by_buyer)
		WHERE buyer_acct = ? AND shared = 1 AND created_at >= ? GROUP BY node_acct`, false); err != nil {
		return nil, err
	}
	return out, nil
}
