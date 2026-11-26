package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLinkTokensAndParsing(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		tok, err := newLinkToken()
		if err != nil || !ValidLinkToken(tok) || seen[tok] {
			t.Fatalf("token %q %v", tok, err)
		}
		seen[tok] = true
		c, err := newPairingCode()
		if err != nil || len(c) != 6 || NormalizePairingCode(FormatPairingCode(c)) != c || strings.Count(FormatPairingCode(c), "-") != 1 {
			t.Fatalf("code %q %v", c, err)
		}
	}
	for _, bad := range []string{"", "short", "abcdefghijk!", "abcdefghijklm", "ABCDE-FGHIJ"} {
		if ValidLinkToken(bad) {
			t.Errorf("valid token: %q", bad)
		}
	}
	if got := FormatPairingCode("ABCDEFGHIJKLMNOP"); got != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("an older 16-character code: %q", got)
	}
	if got := InviteLink("https://sharetoken.app/", KindPair, "7kQm3xV9pT2b"); got != "https://sharetoken.app/p#7kQm3xV9pT2b" {
		t.Errorf("link: %q", got)
	}
	for in, want := range map[string]InviteRef{
		"https://sharetoken.app/p#7kQm3xV9pT2b":         {Kind: "p", Token: "7kQm3xV9pT2b", Host: "sharetoken.app"},
		" https://dev.sharetoken.app/s#Hx82LpQe0aZt \n": {Kind: "s", Token: "Hx82LpQe0aZt", Host: "dev.sharetoken.app"},
		"http://127.0.0.1:18092/p/#7kQm3xV9pT2b":        {Kind: "p", Token: "7kQm3xV9pT2b", Host: "127.0.0.1:18092"},
		"sharetoken://p/7kQm3xV9pT2b":                   {Kind: "p", Token: "7kQm3xV9pT2b"},
		"sharetoken://s/Hx82LpQe0aZt/?h=sharetoken.app": {Kind: "s", Token: "Hx82LpQe0aZt", Host: "sharetoken.app"},
		"SHARETOKEN://P/7kQm3xV9pT2b":                   {Kind: "p", Token: "7kQm3xV9pT2b"},
		"sharetoken:p/7kQm3xV9pT2b":                     {Code: "sharetoken:p/7kQm3xV9pT2b"},
		"https://sharetoken.app/docs/#x":                {Token: "x", Host: "sharetoken.app"},
		"7kQm3xV9pT2b":                                  {Token: "7kQm3xV9pT2b"},
		"abcde-fghij":                                   {Code: "abcde-fghij"},
		"ABCDEFGHIJ":                                    {Code: "ABCDEFGHIJ"},
	} {
		if got := ParseInvite(in); got != want {
			t.Errorf("ParseInvite(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestInviteLinksAndApproval(t *testing.T) {
	s := testStore(t)
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	user := func(name string) *User {
		t.Helper()
		u, err := s.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	seller, b1, b2, b3 := user("seller"), user("buyer1"), user("buyer2"), user("buyer3")

	// A link pairs as the code does, and its page sees who it is from.
	open, err := s.CreatePairingCodeWith(seller.ID, PairingCodeOptions{Label: "friends"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PreviewInvite(KindPair, open.LinkToken)
	if err != nil || p.Inviter != "seller" || p.Label != "friends" || p.Approval || p.ExpiresAt != open.ExpiresAt || p.Kind != KindPair {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if _, err := s.PreviewInvite(KindShare, open.LinkToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a pairing token as a share invite: %v", err)
	}
	if _, err := s.PreviewInvite(KindPair, open.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code is no token: %v", err)
	}
	if _, err := s.RedeemPairingLink(b1.ID, open.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code as a token: %v", err)
	}
	if _, err := s.RedeemPairingLink(seller.ID, open.LinkToken); !errors.Is(err, ErrOwnCode) {
		t.Fatalf("own link: %v", err)
	}
	got, err := s.RedeemPairingLink(b1.ID, open.LinkToken)
	if err != nil || got.ID != seller.ID || got.Pending || got.Label != "friends" {
		t.Fatalf("redeem a link: %+v %v", got, err)
	}

	// A code that asks for approval pairs nobody until the seller approves.
	ask, err := s.CreatePairingCodeWith(seller.ID, PairingCodeOptions{MaxUses: 1, Approval: true})
	if err != nil || !ask.Approval {
		t.Fatalf("approval code: %+v %v", ask, err)
	}
	if p, _ := s.PreviewInvite(KindPair, ask.LinkToken); p == nil || !p.Approval {
		t.Fatalf("preview of an approval code: %+v", p)
	}
	got, err = s.RedeemPairingCode(b2.ID, ask.Code)
	if err != nil || !got.Pending || !got.NewRequest || got.Since != now.UnixNano() {
		t.Fatalf("request: %+v %v", got, err)
	}
	now = now.Add(time.Minute)
	got, err = s.RedeemPairingLink(b2.ID, ask.LinkToken)
	if err != nil || !got.Pending || got.NewRequest || got.Since != now.Add(-time.Minute).UnixNano() {
		t.Fatalf("the same request again: %+v %v", got, err)
	}
	if sellers, _, _ := s.Pairings(b2.ID); len(sellers) != 0 {
		t.Fatalf("paired before approval: %+v", sellers)
	}
	reqs, err := s.InviteRequests(seller.ID, KindPair)
	if err != nil || len(reqs) != 1 || reqs[0].UserID != b2.ID || reqs[0].Username != "buyer2" || reqs[0].Kind != KindPair {
		t.Fatalf("requests: %+v %v", reqs, err)
	}
	if w, _ := s.WaitingRequests(b2.ID, KindPair); len(w) != 1 || w[0].Username != "seller" || w[0].ID != reqs[0].ID {
		t.Fatalf("waiting: %+v", w)
	}
	if other, _ := s.InviteRequests(seller.ID, KindShare); len(other) != 0 {
		t.Fatalf("share requests: %+v", other)
	}
	// Only the seller approves; the request goes, the buyer is paired and the code is used up.
	if _, err := s.ApproveRequest(b2.ID, reqs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approved by the buyer: %v", err)
	}
	if r, err := s.ApproveRequest(seller.ID, reqs[0].ID); err != nil || r.UserID != b2.ID {
		t.Fatalf("approve: %+v %v", r, err)
	}
	if sellers, _, _ := s.Pairings(b2.ID); len(sellers) != 1 || sellers[0].ID != seller.ID {
		t.Fatalf("not paired after approval: %+v", sellers)
	}
	if reqs, _ := s.InviteRequests(seller.ID, KindPair); len(reqs) != 0 {
		t.Fatalf("request kept: %+v", reqs)
	}
	if _, err := s.RedeemPairingCode(b3.ID, ask.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a used-up approval code: %v", err)
	}

	// A request whose code no longer admits anyone is dropped when approved.
	ask2, _ := s.CreatePairingCodeWith(seller.ID, PairingCodeOptions{MaxUses: 1, Approval: true})
	ask3, _ := s.CreatePairingCodeWith(seller.ID, PairingCodeOptions{Approval: true, TTL: MaxPairingTTL})
	if _, err := s.RedeemPairingCode(b3.ID, ask2.Code); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePairingCode(seller.ID, ask2.ID); err != nil {
		t.Fatal(err)
	}
	reqs, _ = s.InviteRequests(seller.ID, KindPair)
	if len(reqs) != 1 {
		t.Fatalf("requests: %+v", reqs)
	}
	if _, err := s.ApproveRequest(seller.ID, reqs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve on a revoked code: %v", err)
	}
	if reqs, _ := s.InviteRequests(seller.ID, KindPair); len(reqs) != 0 {
		t.Fatalf("a dead request was kept: %+v", reqs)
	}
	// Rejected, withdrawn and expired requests go too.
	if _, err := s.RedeemPairingCode(b3.ID, ask3.Code); err != nil {
		t.Fatal(err)
	}
	reqs, _ = s.InviteRequests(seller.ID, KindPair)
	if err := s.RejectRequest(b3.ID, reqs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected by the buyer: %v", err)
	}
	if err := s.RejectRequest(seller.ID, reqs[0].ID); err != nil {
		t.Fatal(err)
	}
	// Asking again with the same code waits: each request mails the seller.
	// Another user, and another code, do not wait.
	if _, err := s.RedeemPairingCode(b3.ID, ask3.Code); !errors.Is(err, ErrCooldown) {
		t.Fatalf("asked again right after a rejection: %v", err)
	}
	if w, _ := s.WaitingRequests(b3.ID, KindPair); len(w) != 0 {
		t.Fatalf("a refused request was filed: %+v", w)
	}
	now = now.Add(RejectCooldown - time.Minute)
	if _, err := s.RedeemPairingCode(b3.ID, ask3.Code); !errors.Is(err, ErrCooldown) {
		t.Fatalf("asked again before the wait was over: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := s.RedeemPairingCode(b3.ID, ask3.Code); err != nil {
		t.Fatal(err)
	}
	w, _ := s.WaitingRequests(b3.ID, KindPair)
	if err := s.WithdrawRequest(seller.ID, w[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("withdrawn by the seller: %v", err)
	}
	if err := s.WithdrawRequest(b3.ID, w[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(b3.ID, ask3.Code); !errors.Is(err, ErrCooldown) {
		t.Fatalf("asked again right after withdrawing: %v", err)
	}
	now = now.Add(WithdrawCooldown)
	if _, err := s.RedeemPairingCode(b3.ID, ask3.Code); err != nil {
		t.Fatal(err)
	}
	// The waits that are over are swept when the next one starts.
	var waits int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM invite_cooldowns`).Scan(&waits); err != nil || waits != 1 {
		t.Fatalf("%d waits kept, want the last one: %v", waits, err)
	}
	reqs, _ = s.InviteRequests(seller.ID, KindPair)
	now = now.Add(RequestTTL + time.Second)
	if r, _ := s.InviteRequests(seller.ID, KindPair); len(r) != 0 {
		t.Fatalf("an expired request is listed: %+v", r)
	}
	if _, err := s.ApproveRequest(seller.ID, reqs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve an expired request: %v", err)
	}

	// A disabled seller's links say nothing.
	fresh, _ := s.CreatePairingCodeWith(seller.ID, PairingCodeOptions{})
	if err := s.SetDisabled(seller.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewInvite(KindPair, fresh.LinkToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preview of a disabled seller's link: %v", err)
	}
}

func TestShareInviteLinksAndApproval(t *testing.T) {
	s := testStore(t)
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	user := func(name string) *User {
		t.Helper()
		u, err := s.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	alice, bob, carol, dave := user("alice"), user("bob"), user("carol"), user("dave")

	inv, err := s.CreateShareInvite(alice.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.PreviewInvite(KindShare, inv.LinkToken); err != nil || p.Inviter != "alice" || p.Kind != KindShare {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if p, err := s.JoinShareLink(bob.ID, inv.LinkToken); err != nil || p.ID != alice.ID || p.Pending || p.GroupID == 0 {
		t.Fatalf("join by link: %+v %v", p, err)
	}
	if _, err := s.PreviewInvite(KindShare, inv.LinkToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preview of a used invite: %v", err)
	}

	// With approval, joining files a request; the invite stays open until the
	// inviter approves one, and then the other requests lose it.
	ask, err := s.CreateShareInviteWith(alice.ID, 0, true)
	if err != nil || !ask.Approval {
		t.Fatalf("approval invite: %+v %v", ask, err)
	}
	if invs, _ := s.ShareInvites(alice.ID); len(invs) != 1 || !invs[0].Approval || invs[0].LinkToken != ask.LinkToken {
		t.Fatalf("invites: %+v", invs)
	}
	if p, err := s.JoinShareLink(carol.ID, ask.LinkToken); err != nil || !p.Pending || !p.NewRequest {
		t.Fatalf("request: %+v %v", p, err)
	}
	if p, err := s.JoinShare(dave.ID, ask.Code); err != nil || !p.Pending {
		t.Fatalf("second request: %+v %v", p, err)
	}
	if ps, _ := s.SharePartners(carol.ID); len(ps) != 0 {
		t.Fatalf("partners before approval: %+v", ps)
	}
	reqs, err := s.InviteRequests(alice.ID, KindShare)
	if err != nil || len(reqs) != 2 || reqs[0].UserID != carol.ID || reqs[1].UserID != dave.ID {
		t.Fatalf("requests: %+v %v", reqs, err)
	}
	if _, err := s.ApproveRequest(alice.ID, reqs[0].ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if ps, _ := s.SharePartners(carol.ID); len(ps) != 1 || ps[0].ID != alice.ID {
		t.Fatalf("partners after approval: %+v", ps)
	}
	if _, err := s.ApproveRequest(alice.ID, reqs[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve on a used invite: %v", err)
	}
	if r, _ := s.InviteRequests(alice.ID, KindShare); len(r) != 0 {
		t.Fatalf("requests left: %+v", r)
	}

	// One user has at most MaxWaitingRequests requests waiting.
	for i := range MaxWaitingRequests + 1 {
		o := user("owner" + string(rune('a'+i)))
		c, err := s.CreatePairingCodeWith(o.ID, PairingCodeOptions{Approval: true})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.RedeemPairingCode(dave.ID, c.Code)
		if i < MaxWaitingRequests-1 && err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if i == MaxWaitingRequests && !errors.Is(err, ErrLimit) {
			t.Fatalf("request past the limit: %v", err)
		}
	}
}
