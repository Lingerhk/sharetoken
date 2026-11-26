package coordinator

import (
	"testing"
	"time"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// Tickets that never started are capped for an account in all and per node,
// whatever their age, and although every device that took one is gone (which
// is how the stale-ticket count was got around: it skips the tickets of a
// device that reconnected). Other buyers keep being served.
func TestIdleTicketsAreCapped(t *testing.T) {
	f := newFixture(t, Config{MaxIdleTickets: 5, MaxIdlePerNode: 2,
		ConnectedSince: func(string) time.Time { return time.Time{} }})
	a, b, c, hoarder, buyer := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	nodes := []*p2p.Keys{a, b, c}
	for _, n := range nodes {
		f.offer(n, "m", priceIn, priceOut, 100)
	}
	f.deposit(hoarder, 1_000_000)
	f.deposit(buyer, 1_000_000)
	req := protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1}
	perNode := map[string]int{}
	var first *protocol.Ticket
	for i := range 5 {
		tr, err := f.eng.IssueTicket(hoarder.ID(), req)
		if err != nil {
			t.Fatalf("ticket %d: %v", i+1, err)
		}
		if first == nil {
			first = &tr.Ticket
		}
		if perNode[tr.Ticket.Node]++; perNode[tr.Ticket.Node] > 2 {
			t.Fatalf("a third unstarted ticket for node %s", tr.Ticket.Node)
		}
	}
	if _, err := f.eng.IssueTicket(hoarder.ID(), req); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("a sixth unstarted ticket: want capacity, got %v", err)
	}
	// Neither age nor a new offer changes it, while the tickets live.
	f.now = f.now.Add(40 * time.Second)
	for _, n := range nodes {
		f.offer(n, "m", priceIn, priceOut, 100)
	}
	if _, err := f.eng.IssueTicket(hoarder.ID(), req); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("after 40 s: want capacity, got %v", err)
	}
	// The nodes still have 95 free slots each for everyone else.
	for range 5 {
		if _, err := f.eng.IssueTicket(buyer.ID(), req); err != nil {
			t.Fatalf("another buyer: %v", err)
		}
	}
	if _, err := f.eng.IssueTicket(buyer.ID(), req); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("the other buyer is capped the same way: %v", err)
	}
	// A ticket that starts no longer counts.
	for _, n := range nodes {
		if n.ID() == first.Node {
			if err := f.eng.MarkStarted(n.ID(), *first); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.eng.IssueTicket(hoarder.ID(), req); err != nil {
		t.Fatalf("after one started: %v", err)
	}
	// Nor do released ones: all of them, once their tickets expired.
	f.now = f.now.Add(2 * time.Minute)
	f.eng.Tick(f.now)
	for _, n := range nodes {
		f.offer(n, "m", priceIn, priceOut, 100)
	}
	if _, err := f.eng.IssueTicket(hoarder.ID(), req); err != nil {
		t.Fatalf("after the tickets expired: %v", err)
	}
}

// One node: the buyer's ninth unstarted ticket for it waits, by default, and
// with several nodes a burst of 32 requests gets its tickets at once.
func TestIdleTicketDefaultsLeaveRoomForABurst(t *testing.T) {
	f := newFixture(t, Config{})
	buyer := mustKeys(t)
	f.deposit(buyer, 1_000_000)
	node := mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 100)
	req := protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1}
	var tickets []protocol.Ticket
	for i := range 8 {
		tr, err := f.eng.IssueTicket(buyer.ID(), req)
		if err != nil {
			t.Fatalf("ticket %d for one node: %v", i+1, err)
		}
		tickets = append(tickets, tr.Ticket)
	}
	if _, err := f.eng.IssueTicket(buyer.ID(), req); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("a ninth unstarted ticket for one node: want capacity (the client waits), got %v", err)
	}
	// They start, as they do within seconds, and the next eight follow.
	for _, tk := range tickets {
		if err := f.eng.MarkStarted(node.ID(), tk); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 8 {
		if _, err := f.eng.IssueTicket(buyer.ID(), req); err != nil {
			t.Fatalf("ticket %d after the first eight started: %v", i+1, err)
		}
	}
	for range 3 {
		f.offer(mustKeys(t), "m", priceIn, priceOut, 100)
	}
	for i := 8; i < 32; i++ {
		if _, err := f.eng.IssueTicket(buyer.ID(), req); err != nil {
			t.Fatalf("unstarted ticket %d over four nodes: %v", i+1, err)
		}
	}
	if _, err := f.eng.IssueTicket(buyer.ID(), req); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("a 33rd unstarted ticket: want capacity, got %v", err)
	}
}
