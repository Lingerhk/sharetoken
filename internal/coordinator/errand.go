package coordinator

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/ledger"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
)

// Errands: the coordinator's own jobs (audits), carried by users' clients.
//
// A job sent from the coordinator's machine is easy for a node to tell from
// a buyer's: it comes from an address and a peer ID the node has never
// served, and never over a direct connection. A carried job comes from a
// device that buys on the market anyway, the way that device always
// connects. See protocol.ErrandJob for what the carrier can and cannot do.
//
// The coordinator never dials anyone: clients ask for errands (Errands.poll),
// which also tells the coordinator which clients are there to carry one.

// AuditAccount pays for carried jobs. Each is funded out of the platform's
// fees when its ticket is issued; what comes back goes back (Tick).
var AuditAccount = ledger.Account("audit")

// IssueErrandTicket issues a ticket for a job of the platform's own that
// carrier will run on req.Node: the ticket names carrier as the buyer, as the
// node requires, and AuditAccount pays. It fails unless carrier could have
// been matched to that node on the market (ErrandCarriers).
func (e *Engine) IssueErrandTicket(carrier string, req protocol.TicketRequest) (*protocol.TicketResponse, error) {
	if req.Node == "" {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "an errand names its node")
	}
	req.Exclude, req.PreferNode, req.MaxPriceIn, req.MaxPriceOut = nil, "", 0, 0
	return e.issueTicket(carrier, req, true)
}

// ErrandCarriers returns those of devices that may carry a job to node: a
// user's device (any peer where accounts are open) that the market could
// match to the node for money. Not the node's own user, not a buyer whose
// pairings exclude the node, not a stranger to a private node, and not a
// share partner (it would be served for free, and is the seller's friend).
// When some of them have bought on the market before, only those are
// returned: a node has more reason to take them for buyers.
func (e *Engine) ErrandCarriers(node string, devices []string) ([]string, error) {
	now := e.cfg.Now()
	var out []string
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		oe, ok := e.offers[node]
		if !ok || oe.offer.ExpiresAt <= now.UnixNano() {
			return nil
		}
		for _, d := range devices {
			if d == node {
				continue
			}
			acct, err := e.accountOf(tx, d)
			if protocol.CodeOf(err) == protocol.CodeUnauthorized {
				continue
			}
			if err != nil {
				return err
			}
			sc, err := scopeOf(tx, d, now)
			if err != nil {
				return err
			}
			if _, partner := sc.partners[oe.acct]; partner || sc.auditor || sc.onlyPartners || acct == oe.acct || !sc.allows(oe.acct) {
				continue
			}
			out = append(out, d)
		}
		if len(out) < 2 {
			return nil
		}
		rows, err := tx.Query(`SELECT DISTINCT buyer FROM jobs WHERE status = 'final' AND buyer_acct != ? AND buyer IN (?`+strings.Repeat(",?", len(out)-1)+`)`,
			append([]any{AuditAccount}, anySlice(out)...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var bought []string
		for rows.Next() {
			var b string
			if err := rows.Scan(&b); err != nil {
				return err
			}
			bought = append(bought, b)
		}
		if len(bought) > 0 {
			out = bought
		}
		return rows.Err()
	})
	slices.Sort(out)
	return out, err
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// returnAuditFunds moves what carried jobs did not spend back to the
// platform's fees.
func (e *Engine) returnAuditFunds() error {
	return e.update(func(_ *sql.Tx, led *ledger.Tx) error {
		b, err := led.Balance(AuditAccount)
		if err != nil || b <= 0 {
			return err
		}
		return led.Transfer(AuditAccount, ledger.PlatformFees, b, "audit funds returned")
	})
}

// ErrandResult is what a carrier handed back.
type ErrandResult struct {
	Receipt *receipt.SignedReceipt
	Chunks  [][]byte // the sealed response frames, in order
	Error   string   // why there is nothing: the carrier's word, not evidence
	Relayed bool
	Via     string
}

// Errands is the board carried jobs wait on.
type Errands struct {
	mu        sync.Mutex
	pollAfter time.Duration
	now       func() time.Time
	seen      map[string]time.Time   // device -> its last poll
	benched   map[string]time.Time   // device -> not used as a carrier until then
	waiting   map[string]*errandTask // device -> the job its next poll gets
	out       map[string]*errandTask // job ID -> a job handed to its carrier
}

type errandTask struct {
	device string
	// make builds the job when its carrier asks: a ticket issued earlier
	// would have aged, or expired, while waiting for the poll.
	make func() (*protocol.ErrandJob, error)
	done chan ErrandResult
}

// idleErrandPoll is how often clients ask while nothing sends errands.
const idleErrandPoll = 10 * time.Minute

// Limits of what a carrier may hand back: audits ask for short answers.
const (
	maxErrandChunks = 4096
	maxErrandBytes  = 16 << 20
)

func newErrands() *Errands {
	return &Errands{pollAfter: idleErrandPoll, now: time.Now, seen: map[string]time.Time{}, benched: map[string]time.Time{},
		waiting: map[string]*errandTask{}, out: map[string]*errandTask{}}
}

// SetPollAfter sets how often clients are told to ask (whoever posts errands
// sets it: a job waits for its carrier's next poll).
func (b *Errands) SetPollAfter(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pollAfter = max(d, time.Second)
}

// Window is how long after a poll a device counts as there: until its next
// poll is due, with slack for jitter and a slow path.
func (b *Errands) Window() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.window()
}

func (b *Errands) window() time.Duration { return b.pollAfter + b.pollAfter/2 + 15*time.Second }

// Carriers lists the devices that asked recently enough to ask again soon,
// and carry nothing right now.
func (b *Errands) Carriers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var out []string
	for d, at := range b.seen {
		if now.Sub(at) > b.window() {
			delete(b.seen, d)
			continue
		}
		if until, ok := b.benched[d]; ok {
			if now.Before(until) {
				continue
			}
			delete(b.benched, d)
		}
		if b.waiting[d] == nil && !b.carrying(d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// Bench keeps device from carrying for d: it took a job and did not bring
// back what the node signed for. The node was paid for that job by the
// platform, so a carrier is not given many chances to do it.
func (b *Errands) Bench(device string, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.benched[device] = b.now().Add(d)
}

func (b *Errands) carrying(device string) bool {
	for _, t := range b.out {
		if t.device == device {
			return true
		}
	}
	return false
}

// Post gives device a job at its next poll: make builds it then. The result
// arrives on the channel (an Error when make failed or the answer could not
// be sent); withdraw takes the job back once nobody waits for it.
func (b *Errands) Post(device string, build func() (*protocol.ErrandJob, error)) (result <-chan ErrandResult, withdraw func(), err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.waiting[device] != nil || b.carrying(device) {
		return nil, nil, errors.New("coordinator: the device already has an errand")
	}
	t := &errandTask{device: device, make: build, done: newResult()}
	b.waiting[device] = t
	return t.done, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.waiting[device] == t {
			delete(b.waiting, device)
		}
		for id, o := range b.out {
			if o == t {
				delete(b.out, id)
			}
		}
	}, nil
}

func newResult() chan ErrandResult { return make(chan ErrandResult, 1) }

// poll answers a device's ErrandPoll.
func (b *Errands) poll(device string) protocol.Errand {
	b.mu.Lock()
	b.seen[device] = b.now()
	resp := protocol.Errand{PollAfter: uint32(b.pollAfter / time.Second)}
	t := b.waiting[device]
	delete(b.waiting, device)
	b.mu.Unlock()
	if t == nil {
		return resp
	}
	job, err := t.make()
	if err != nil {
		t.done <- ErrandResult{Error: "the job could not be made: " + err.Error()}
		return resp
	}
	b.mu.Lock()
	b.out[job.Ticket.JobID] = t
	b.mu.Unlock()
	resp.Job = job
	return resp
}

// unsent ends a job whose poll answer did not reach its device.
func (b *Errands) unsent(device, jobID string) {
	b.deliver(device, jobID, ErrandResult{Error: "the job did not reach its carrier"})
}

// carried reports whether device carries job jobID.
func (b *Errands) carried(device, jobID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.out[jobID]
	return t != nil && t.device == device
}

// deliver hands a carrier's result to whoever posted the job.
func (b *Errands) deliver(device, jobID string, res ErrandResult) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.out[jobID]
	if t == nil || t.device != device {
		return false
	}
	delete(b.out, jobID)
	t.done <- res
	return true
}
