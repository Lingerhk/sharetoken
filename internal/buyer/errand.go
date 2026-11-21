package buyer

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	lproto "github.com/libp2p/go-libp2p/core/protocol"

	"sharetoken/internal/box"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// Errands: jobs of the coordinator's own that this client carries (see
// protocol.ErrandJob). The coordinator audits nodes with them; coming from an
// ordinary client, over that client's ordinary connection, a node cannot tell
// them from a buyer's job. The client sees neither the request nor the
// answer (both are sealed with a key the coordinator keeps), pays nothing,
// and cannot alter what it carries: the node's receipt signs the hashes.
//
// What carrying costs the user: a little traffic, and that the node sees the
// device's peer ID and address, as it does for any job the user buys.

// maxCarried bounds the response frames of a carried job (audits ask for
// short answers).
const maxCarried = 16 << 20

// Poll timing: the first poll comes soon after the client starts, later ones
// when the coordinator says; an unreachable or older coordinator is asked
// again after errandRetry.
const (
	defaultErrandFirst = 20 * time.Second
	errandRetry        = 10 * time.Minute
	maxErrandWait      = time.Hour
)

// RunErrands asks the coordinator for errands until ctx ends and carries the
// ones it gets, one at a time. enabled, if set, is asked before every poll:
// while it says no, the client does not ask (the user opted out). first is
// the delay before the first poll (0: 20 s).
func (c *Client) RunErrands(ctx context.Context, first time.Duration, enabled func() bool) {
	wait := first
	if wait <= 0 {
		wait = defaultErrandFirst
	}
	for {
		// Jitter: polls must not line up with anything a node could observe.
		d := wait*3/4 + time.Duration(rand.Int64N(int64(wait)/2+1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		if enabled != nil && !enabled() {
			wait = time.Minute
			continue
		}
		var e protocol.Errand
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		// Not c.call: a failed errand poll is no reason to switch relays.
		err := p2p.Call(pctx, c.h, c.coordinator.ID, protocol.CtrlProtocol, protocol.TypeErrandPoll, protocol.ErrandPoll{}, protocol.TypeErrand, &e)
		cancel()
		if err != nil {
			wait = errandRetry
			continue
		}
		wait = min(max(time.Duration(e.PollAfter)*time.Second, time.Second), maxErrandWait)
		if e.Job == nil {
			continue
		}
		res, err := c.Carry(ctx, e.Job)
		if ctx.Err() != nil {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if rerr := c.reportErrand(rctx, e.Job.Ticket.JobID, res, err); rerr != nil {
			c.log.Info("could not hand back an errand", "job", e.Job.Ticket.JobID, "err", rerr)
		}
		cancel()
	}
}

// Carry runs a job the coordinator sealed, as a job of this client's own:
// same dial, same frames, same acknowledgement. The Result holds the node's
// receipt and the sealed response frames (Sealed). The ticket is checked like
// any other: issued by the coordinator to this device, for the node whose
// signed offer comes with it.
func (c *Client) Carry(ctx context.Context, job *protocol.ErrandJob) (*Result, error) {
	t := &job.Ticket
	req := ChatRequest{Model: t.Model, API: t.API, BudgetMicros: t.ReservedMicros, Node: t.Node}
	if _, err := c.verifyTicket(&protocol.TicketResponse{Ticket: job.Ticket, Offer: job.Offer}, req); err != nil {
		return nil, err
	}
	if len(job.BuyerBoxPub) != box.PublicKeySize || len(job.Request) > protocol.MaxErrandRequest || !bytes.HasPrefix(job.Request, job.BuyerBoxPub) {
		return nil, errors.New("buyer: malformed errand")
	}
	var tm Timing
	start := time.Now()
	tm.Attempts = 1
	res, _, err := c.runJob(ctx, t, nil, req, nil, &tm, job)
	tm.TotalMs = ms(time.Since(start))
	if res != nil {
		res.Timing = tm
	}
	c.logJob(t, &tm, err)
	return res, err
}

// reportErrand hands a carried job back to the coordinator: the receipt and
// the sealed frames, or why there are none.
func (c *Client) reportErrand(ctx context.Context, jobID string, res *Result, runErr error) error {
	rep := protocol.ErrandReport{JobID: jobID}
	var chunks [][]byte
	if res != nil {
		if len(res.Receipt.Sig) > 0 {
			rep.Receipt = &res.Receipt
		}
		chunks = res.Sealed
		rep.Chunks, rep.Relayed, rep.Via = uint64(len(chunks)), res.Timing.Relayed, res.Timing.Via
	}
	if runErr != nil {
		rep.Error = runErr.Error()
		if len(rep.Error) > 500 {
			rep.Error = rep.Error[:500]
		}
	}
	st, err := c.h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-errand"), c.coordinator.ID, lproto.ID(protocol.CtrlProtocol))
	if err != nil {
		return err
	}
	defer st.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	}
	if err := protocol.SendRequest(st, protocol.TypeErrandReport, rep); err != nil {
		_ = st.Reset()
		return err
	}
	for i, ch := range chunks {
		if err := protocol.SendData(st, uint64(i), ch); err != nil {
			_ = st.Reset()
			return err
		}
	}
	_ = st.CloseWrite()
	env, err := protocol.Recv(st)
	if err != nil {
		return err
	}
	return env.Decode(protocol.TypeOK, nil)
}
