package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/portal"
)

// Invite links (package accounts, invites.go): a pairing code or share invite
// also travels as a link, https://<portal>/p#<token> or /s#<token>. The
// desktop app registers sharetoken:// with the system, so the link's page can
// hand it the invite as sharetoken://p/<token>?h=<portal host>; the shell
// passes it here (POST /api/v1/invite/pending), and the app shows it and asks
// before accepting anything: any web page can open such a link.
//
//	GET    /api/v1/invite/pending    the invite waiting to be shown ({} when none)
//	POST   /api/v1/invite/pending    {link}: keep it until it is accepted or dismissed
//	DELETE /api/v1/invite/pending    dismiss it
//	GET    /api/v1/invite/preview    ?link=: who invites (from the portal; no sign-in needed)
//	POST   /api/v1/invite/accept     {link}: accept, as the signed-in user → portal.AcceptedInvite

// PendingInvite is an invite link the app was opened with.
type PendingInvite struct {
	Kind  string `json:"kind"`
	Token string `json:"token"`
	// Link is the invite on the portal this app uses.
	Link string `json:"link"`
	// Host is the portal the link came from, when it says; OtherPortal: that
	// is not the portal this app uses, where the invite cannot be found.
	Host        string `json:"host,omitempty"`
	OtherPortal bool   `json:"other_portal,omitempty"`
	At          int64  `json:"at"`
}

// pendingInvites holds the invite the app was last opened with.
type pendingInvites struct {
	mu  sync.Mutex
	inv *PendingInvite
}

func (p *pendingInvites) get() *PendingInvite {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inv == nil {
		return nil
	}
	c := *p.inv
	return &c
}

func (p *pendingInvites) set(inv *PendingInvite) {
	p.mu.Lock()
	p.inv = inv
	p.mu.Unlock()
}

// hostOf is the host of a portal address, without a default port.
func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(h, ":443")) || (u.Scheme == "http" && strings.HasSuffix(h, ":80")) {
		h = h[:strings.LastIndexByte(h, ':')]
	}
	return h
}

// PendingFromLink reads a link, as clicked or pasted, for the portal at base.
func PendingFromLink(link, base string) (*PendingInvite, error) {
	ref := accounts.ParseInvite(link)
	if ref.Token == "" || ref.Kind == "" || !accounts.ValidLinkToken(ref.Token) {
		return nil, errors.New("not an invite link: expected https://<portal>/p#<token>, /s#<token> or sharetoken://p/<token>")
	}
	inv := &PendingInvite{Kind: ref.Kind, Token: ref.Token, Host: strings.ToLower(ref.Host), Link: accounts.InviteLink(base, ref.Kind, ref.Token), At: time.Now().UnixNano()}
	if inv.Host != "" && hostOf(base) != "" && inv.Host != hostOf(base) {
		inv.OtherPortal = true
	}
	return inv, nil
}

// currentPortal is the portal the daemon speaks to: the signed-in user's, or,
// signed out, the one the sign-in page would use.
func (d *Daemon) currentPortal() (pc *PortalClient, base string) {
	d.mu.Lock()
	pc, base, pin := d.portal, d.cfg.Portal, d.cfg.PortalPin
	d.mu.Unlock()
	if pc != nil {
		return pc, base
	}
	if base == "" {
		base, pin = PortalAddr(), ""
	}
	return NewPortalClient(base, "", pin), base
}

func (d *Daemon) inviteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/invite/pending", func(w http.ResponseWriter, r *http.Request) {
		if inv := d.invites.get(); inv != nil {
			writeJSON(w, http.StatusOK, inv)
			return
		}
		writeJSON(w, http.StatusOK, struct{}{})
	})
	mux.HandleFunc("POST /api/v1/invite/pending", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Link string `json:"link"`
	}) {
		_, base := d.currentPortal()
		inv, err := PendingFromLink(req.Link, base)
		if err != nil {
			writeErrorCode(w, http.StatusBadRequest, "bad_link", err.Error())
			return
		}
		d.invites.set(inv)
		d.log.Info("opened with an invite link", "kind", inv.Kind, "other_portal", inv.OtherPortal)
		writeJSON(w, http.StatusOK, inv)
	}))
	mux.HandleFunc("DELETE /api/v1/invite/pending", func(w http.ResponseWriter, r *http.Request) {
		d.invites.set(nil)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/invite/preview", func(w http.ResponseWriter, r *http.Request) {
		pc, base := d.currentPortal()
		inv, err := PendingFromLink(r.URL.Query().Get("link"), base)
		if err != nil {
			writeErrorCode(w, http.StatusBadRequest, "bad_link", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		p, err := pc.PreviewInvite(ctx, inv.Kind, inv.Token)
		if err != nil {
			status, code := http.StatusBadGateway, "portal_unreachable"
			var pe *PortalError
			if errors.As(err, &pe) {
				status, code = pe.Status, pe.Code
			}
			writeErrorCode(w, status, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
	mux.HandleFunc("POST /api/v1/invite/accept", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Link string `json:"link"`
		Kind string `json:"kind"`
		Code string `json:"code"`
	}) {
		d.mu.Lock()
		pc := d.portal
		d.mu.Unlock()
		if pc == nil {
			writeErrorCode(w, http.StatusUnauthorized, "not_logged_in", "not logged in")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var out portal.AcceptedInvite
		if err := pc.Do(ctx, http.MethodPost, "/api/v1/invite/accept", req, &out); err != nil {
			status, code, msg := http.StatusBadGateway, "portal_unreachable", err.Error()
			var pe *PortalError
			if errors.As(err, &pe) {
				status, code, msg = pe.Status, pe.Code, pe.Message
			}
			if IsUnauthorized(err) {
				d.expire(pc.Token, "the portal rejected the login (expired or revoked)")
			}
			writeErrorCode(w, status, code, msg)
			return
		}
		// The invite the app was opened with is done once it is accepted.
		if inv := d.invites.get(); inv != nil && accounts.ParseInvite(req.Link).Token == inv.Token {
			d.invites.set(nil)
		}
		status := http.StatusOK
		if out.Pending {
			status = http.StatusAccepted
		}
		writeJSON(w, status, out)
	}))
}

// ---- the portal's API, for the command line ----

// PreviewInvite says who made the invite of kind that token stands for; it
// needs no sign-in.
func (p *PortalClient) PreviewInvite(ctx context.Context, kind, token string) (*accounts.InvitePreview, error) {
	var out accounts.InvitePreview
	err := p.Do(ctx, http.MethodGet, "/api/v1/invite/"+url.PathEscape(kind)+"/"+url.PathEscape(token), nil, &out)
	return &out, err
}

// AcceptInvite accepts an invite link, link token or code of either kind.
func (p *PortalClient) AcceptInvite(ctx context.Context, link string) (*portal.AcceptedInvite, error) {
	var out portal.AcceptedInvite
	err := p.Do(ctx, http.MethodPost, "/api/v1/invite/accept", map[string]string{"link": link}, &out)
	return &out, err
}

// ApproveRequest approves a request waiting for the signed-in user; area is
// "pairing" or "sharing".
func (p *PortalClient) ApproveRequest(ctx context.Context, area string, id int64) (*accounts.InviteRequest, error) {
	var out accounts.InviteRequest
	err := p.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/%s/requests/%d/approve", area, id), nil, &out)
	return &out, err
}

// RejectRequest rejects a request waiting for the signed-in user.
func (p *PortalClient) RejectRequest(ctx context.Context, area string, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/%s/requests/%d", area, id), nil, nil)
}

// WithdrawRequest withdraws one of the signed-in user's own requests.
func (p *PortalClient) WithdrawRequest(ctx context.Context, area string, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/%s/waiting/%d", area, id), nil, nil)
}

func (a *Agent) requestRoutes(mux *http.ServeMux, area string) {
	mux.HandleFunc("POST /api/v1/"+area+"/requests/{id}/approve", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/"+area+"/requests/{id}", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/"+area+"/waiting/{id}", a.portalForward)
}
