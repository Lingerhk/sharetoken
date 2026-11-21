package client

import (
	"encoding/json"
	"strings"
	"testing"

	"sharetoken/internal/accounts"
	"sharetoken/internal/portal"
)

func TestPendingFromLink(t *testing.T) {
	inv, err := PendingFromLink("sharetoken://p/7kQm3xV9pT2b?h=sharetoken.app", "https://sharetoken.app")
	if err != nil || inv.Kind != "p" || inv.Token != "7kQm3xV9pT2b" || inv.Link != "https://sharetoken.app/p#7kQm3xV9pT2b" || inv.OtherPortal {
		t.Fatalf("%+v %v", inv, err)
	}
	inv, err = PendingFromLink("sharetoken://s/7kQm3xV9pT2b?h=dev.sharetoken.app", "https://sharetoken.app:443/")
	if err != nil || !inv.OtherPortal || inv.Host != "dev.sharetoken.app" || inv.Link != "https://sharetoken.app:443/s#7kQm3xV9pT2b" {
		t.Fatalf("another portal: %+v %v", inv, err)
	}
	if inv, err := PendingFromLink("https://127.0.0.1:8080/s#7kQm3xV9pT2b", "http://127.0.0.1:8080"); err != nil || inv.OtherPortal {
		t.Fatalf("same portal with a port: %+v %v", inv, err)
	}
	for _, bad := range []string{"", "ABCDE-FGHIJ", "sharetoken://x/7kQm3xV9pT2b", "sharetoken://p/short", "https://sharetoken.app/docs#7kQm3xV9pT2b"} {
		if _, err := PendingFromLink(bad, "https://sharetoken.app"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The app opened with an invite link: the daemon keeps it, shows who it is
// from even before sign-in, accepts it once signed in, and then forgets it.
func TestDaemonInviteLinks(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	st := &accounts.Store{DB: e.db}
	seller, err := st.CreateUser("seller@example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.CreatePairingCodeWith(seller.ID, accounts.PairingCodeOptions{Label: "team"})
	if err != nil {
		t.Fatal(err)
	}
	d := e.daemon()
	d.want(400, "bad_link", "POST", "/api/v1/invite/pending", map[string]string{"link": "ABCDE-FGHIJ"})
	var none map[string]any
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/invite/pending", nil), &none); err != nil || len(none) != 0 {
		t.Fatalf("nothing pending: %v %v", none, err)
	}

	// Signed out, the portal it would sign in to is the built-in one: a link
	// from the test portal is from elsewhere.
	var inv PendingInvite
	link := "sharetoken://p/" + code.LinkToken + "?h=" + strings.TrimPrefix(e.url, "http://")
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/invite/pending", map[string]string{"link": link}), &inv); err != nil || !inv.OtherPortal {
		t.Fatalf("signed out: %+v %v", inv, err)
	}
	d.signIn(e, "buyer@example.com")
	inv = PendingInvite{}
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/invite/pending", map[string]string{"link": link}), &inv); err != nil ||
		inv.OtherPortal || inv.Kind != "p" || inv.Link != e.url+"/p#"+code.LinkToken {
		t.Fatalf("pending: %+v %v", inv, err)
	}
	var s Session
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/session", nil), &s); err != nil || s.Invite == nil || s.Invite.Token != code.LinkToken {
		t.Fatalf("session: %+v %v", s.Invite, err)
	}
	var p accounts.InvitePreview
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/invite/preview?link="+strings.ReplaceAll(inv.Link, "#", "%23"), nil), &p); err != nil ||
		p.Inviter != "se***@example.com" || p.Label != "team" {
		t.Fatalf("preview: %+v %v", p, err)
	}
	d.want(404, "bad_code", "GET", "/api/v1/invite/preview?link="+strings.ReplaceAll("https://x/p#AAAAAAAAAAAA", "#", "%23"), nil)

	var acc portal.AcceptedInvite
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/invite/accept", map[string]string{"link": inv.Link}), &acc); err != nil ||
		acc.Kind != "p" || acc.ID != seller.ID || acc.Pending {
		t.Fatalf("accept: %+v %v", acc, err)
	}
	s = Session{}
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/session", nil), &s); err != nil || s.Invite != nil {
		t.Fatalf("the invite is still pending after it was accepted: %+v", s.Invite)
	}

	// An invite that asks for approval comes back 202.
	ask, _ := st.CreateShareInviteWith(seller.ID, 0, true)
	if err := json.Unmarshal(d.want(202, "", "POST", "/api/v1/invite/accept", map[string]string{"link": "https://x/s#" + ask.LinkToken}), &acc); err != nil || !acc.Pending || acc.Kind != "s" {
		t.Fatalf("accept with approval: %+v %v", acc, err)
	}
	d.want(200, "", "POST", "/api/v1/invite/pending", map[string]string{"link": link})
	d.want(200, "", "DELETE", "/api/v1/invite/pending", nil)
	s = Session{}
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/session", nil), &s); err != nil || s.Invite != nil {
		t.Fatalf("dismissed: %+v", s.Invite)
	}
	d.want(200, "", "POST", "/api/v1/logout", nil)
	d.want(401, "not_logged_in", "POST", "/api/v1/invite/accept", map[string]string{"link": inv.Link})
}
