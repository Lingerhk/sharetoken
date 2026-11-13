package coordinator

import (
	"testing"

	"sharetoken/internal/accounts"
	"sharetoken/internal/protocol"
)

// The policy in the shared database decides who may start new business.
func TestCheckClient(t *testing.T) {
	f := newFixture(t, Config{})
	f.eng.policy.TTL = 0
	if err := f.eng.CheckClient("0.1.0"); err != nil {
		t.Fatalf("without a policy: %v", err)
	}
	if err := accounts.SaveVersionPolicy(f.eng.db, accounts.VersionPolicy{MinClient: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	for v, want := range map[string]string{
		"0.9.0": protocol.CodeUpgradeRequired, "1.0.0-dev.a": protocol.CodeUpgradeRequired,
		"1.0.0": "", "1.5.0": "",
	} {
		if got := protocol.CodeOf(f.eng.CheckClient(v)); got != want {
			t.Errorf("%s: %q, want %q", v, got, want)
		}
	}
}

// A ticket names a protocol version both sides speak, and is signed over it; a
// buyer and a node with none in common are not matched.
func TestTicketAgreesInferVersion(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	req := protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000}
	tr, err := f.eng.IssueTicket(buyer.ID(), req)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Ticket.InferVersion != protocol.InferVersion || tr.Offer.ClientVersion == "" {
		t.Fatalf("%+v", tr.Ticket)
	}
	forged := tr.Ticket
	forged.InferVersion++
	if err := forged.Verify(f.eng.self); err == nil {
		t.Fatal("the ticket's protocol version is not signed")
	}
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: []uint32{99}, Model: "m", BudgetMicros: 1000}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("no common version: %v", err)
	}
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 1000}); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Fatalf("no versions at all: %v", err)
	}
}

func TestBestInferVersion(t *testing.T) {
	if got := protocol.BestInferVersion([]uint32{1, 2, 3}, []uint32{2, 3, 4}); got != 3 {
		t.Fatal(got)
	}
	if got := protocol.BestInferVersion([]uint32{1}, []uint32{2}); got != 0 {
		t.Fatal(got)
	}
}
