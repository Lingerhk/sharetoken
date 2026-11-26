package coordinator

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/protocol"
)

// priceCap is the most an audit job pays per million tokens.
type priceCap struct{ prompt, out uint64 }

func (c *priceCap) allows(p protocol.ModelPrice) bool {
	return c == nil || p.Prices().PromptCeiling() <= c.prompt && p.PriceCacheRead <= c.prompt && p.PriceOutMicros <= c.out
}

// auditPriceCap is the price cap of an audit job for model: AuditPriceMultiple
// times the median price among the accounts that offer it now (each at its
// lowest price). The platform pays for audits, and a buyer's own price limit
// does not protect it: without a cap, a seller that nobody buys from at its
// price would still be paid that price for every audit question. nil: nobody
// offers the model. The caller holds e.mu.
//
// A seller that is alone with a model, or runs most of the accounts selling
// it, sets the median itself; Config.AuditDailyMicros bounds what that earns.
func (e *Engine) auditPriceCap(model string, now int64) *priceCap {
	type lowest struct{ prompt, out uint64 }
	accts := map[string]lowest{}
	for _, oe := range e.offers {
		if oe.offer.ExpiresAt <= now {
			continue
		}
		p, ok := oe.offer.Price(model)
		if !ok {
			continue
		}
		l, seen := accts[oe.acct]
		if !seen {
			l = lowest{math.MaxUint64, math.MaxUint64}
		}
		accts[oe.acct] = lowest{min(l.prompt, p.Prices().PromptCeiling()), min(l.out, p.PriceOutMicros)}
	}
	if len(accts) == 0 {
		return nil
	}
	prompts, outs := make([]uint64, 0, len(accts)), make([]uint64, 0, len(accts))
	for _, l := range accts {
		prompts, outs = append(prompts, l.prompt), append(outs, l.out)
	}
	slices.Sort(prompts)
	slices.Sort(outs)
	// The lower middle of an even number: of two sellers, the cheaper one.
	mid := (len(accts) - 1) / 2
	return &priceCap{satMul(prompts[mid], e.cfg.AuditPriceMultiple), satMul(outs[mid], e.cfg.AuditPriceMultiple)}
}

// AuditPriceOK reports whether an audit job may be bought from node for
// model at the node's current price (see auditPriceCap). Auditors skip the
// others: their ticket would be refused.
func (e *Engine) AuditPriceOK(node, model string) bool {
	now := e.cfg.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	oe, ok := e.offers[node]
	if !ok || oe.offer.ExpiresAt <= now {
		return false
	}
	p, ok := oe.offer.Price(model)
	return ok && e.auditPriceCap(model, now).allows(p)
}

func satMul(a, b uint64) uint64 {
	if a != 0 && b > math.MaxUint64/a {
		return math.MaxUint64
	}
	return a * b
}

// auditBudgetKey is the meta row that holds what audit jobs reserved on the
// current day: "<UTC date> <micros>".
const auditBudgetKey = "audit_budget"

// chargeAuditBudget counts hold against the day's audit budget
// (Config.AuditDailyMicros) and refuses it when the budget is spent. It
// counts what audit tickets reserve, which is at least what they pay: the
// budget is the last bound on what audits move out of the platform's fees,
// whatever the prices and however often audits run. It runs in the ticket's
// transaction, so a ticket that is not issued after all is not counted.
func (e *Engine) chargeAuditBudget(tx *sql.Tx, hold uint64, now time.Time) error {
	if hold == 0 {
		return nil
	}
	day := now.UTC().Format(time.DateOnly)
	spent, err := auditBudgetSpent(tx, day)
	if err != nil {
		return err
	}
	if hold > e.cfg.AuditDailyMicros || spent > e.cfg.AuditDailyMicros-hold {
		return protocol.Errorf(protocol.CodeBusy, "audit jobs reserved %d of today's %d micros; no room for %d more", spent, e.cfg.AuditDailyMicros, hold)
	}
	value := fmt.Sprintf("%s %d", day, spent+hold)
	return upsert(tx, "UPDATE meta SET value = ? WHERE `key` = ?", []any{value, auditBudgetKey},
		"INSERT INTO meta (`key`, value) VALUES (?, ?)", []any{auditBudgetKey, value})
}

// AuditBudgetUsed reports what audit jobs reserved today (UTC) and the day's
// budget (operators, tests).
func (e *Engine) AuditBudgetUsed() (used, budget uint64, err error) {
	used, err = auditBudgetSpent(e.db, e.cfg.Now().UTC().Format(time.DateOnly))
	return used, e.cfg.AuditDailyMicros, err
}

// auditBudgetSpent reads what audit jobs reserved on day.
func auditBudgetSpent(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, day string) (uint64, error) {
	var value string
	switch err := q.QueryRow("SELECT value FROM meta WHERE `key` = ?", auditBudgetKey).Scan(&value); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, err
	}
	if d, n, ok := strings.Cut(value, " "); ok && d == day {
		spent, _ := strconv.ParseUint(n, 10, 64)
		return spent, nil
	}
	return 0, nil
}
