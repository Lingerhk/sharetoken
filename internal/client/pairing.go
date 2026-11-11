package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"sharetoken/internal/accounts"
	"sharetoken/internal/portal"
)

// Pairing (package accounts, pairing.go): a seller hands out a pairing code,
// and a buyer that redeems it buys only from the sellers it is paired with.
// The portal keeps the pairings and the coordinator applies them, so nothing
// is stored here: the client asks, and the local API passes requests on.
//
//	GET    /api/v1/pairing                  portal.PairingView: sellers, buyers, codes, restricted
//	POST   /api/v1/pairing/codes            {label, ttl_hours, max_uses} → the new code (seller)
//	DELETE /api/v1/pairing/codes/{id}       revoke a code; pairings made with it stay (seller)
//	POST   /api/v1/pairing/redeem           {code} → the seller now paired with (buyer)
//	DELETE /api/v1/pairing/sellers/{id}     leave a seller (buyer)
//	DELETE /api/v1/pairing/buyers/{id}      remove a buyer (seller)

// Pairing returns who the signed-in user is paired with, and its codes.
func (p *PortalClient) Pairing(ctx context.Context) (*portal.PairingView, error) {
	var out portal.PairingView
	err := p.Do(ctx, http.MethodGet, "/api/v1/pairing", nil, &out)
	return &out, err
}

// NewPairingCode makes a code that pairs buyers with the signed-in user.
// ttlHours 0 is the portal's default; maxUses 0 admits any number of buyers.
func (p *PortalClient) NewPairingCode(ctx context.Context, label string, ttlHours float64, maxUses int64) (*accounts.PairingCode, error) {
	var out accounts.PairingCode
	err := p.Do(ctx, http.MethodPost, "/api/v1/pairing/codes", map[string]any{"label": label, "ttl_hours": ttlHours, "max_uses": maxUses}, &out)
	return &out, err
}

// RevokePairingCode stops a code from admitting more buyers.
func (p *PortalClient) RevokePairingCode(ctx context.Context, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/pairing/codes/%d", id), nil, nil)
}

// RedeemPairingCode pairs the signed-in user, as a buyer, with the seller
// that made code.
func (p *PortalClient) RedeemPairingCode(ctx context.Context, code string) (*accounts.PairedUser, error) {
	var out accounts.PairedUser
	err := p.Do(ctx, http.MethodPost, "/api/v1/pairing/redeem", map[string]string{"code": code}, &out)
	return &out, err
}

// LeaveSeller ends the pairing with a seller; RemoveBuyer ends one with a buyer.
func (p *PortalClient) LeaveSeller(ctx context.Context, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/pairing/sellers/%d", id), nil, nil)
}

func (p *PortalClient) RemoveBuyer(ctx context.Context, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/pairing/buyers/%d", id), nil, nil)
}

func (a *Agent) pairingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/pairing", a.portalPassthrough("/api/v1/pairing"))
	mux.HandleFunc("POST /api/v1/pairing/codes", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/pairing/codes/{id}", a.portalForward)
	mux.HandleFunc("POST /api/v1/pairing/redeem", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/pairing/sellers/{id}", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/pairing/buyers/{id}", a.portalForward)
}

// portalForward sends r to the portal under the same path, with the user's
// token, and answers with the portal's reply. Only for routes registered one
// by one: the path is the caller's.
func (a *Agent) portalForward(w http.ResponseWriter, r *http.Request) {
	var body any
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || !json.Valid(raw) {
			writeErrorCode(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
			return
		}
		body = json.RawMessage(raw)
	}
	var out json.RawMessage
	if err := a.portal.Do(r.Context(), r.Method, r.URL.Path, body, &out); err != nil {
		status, code, msg := http.StatusBadGateway, "portal_unreachable", err.Error()
		if pe, ok := err.(*PortalError); ok {
			status, code, msg = pe.Status, pe.Code, pe.Message
		}
		writeErrorCode(w, status, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}
