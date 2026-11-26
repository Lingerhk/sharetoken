package client

import (
	"context"
	"fmt"
	"net/http"

	"sharetoken/internal/accounts"
	"sharetoken/internal/portal"
)

// Share partners (package accounts, sharing.go): two users who pair up use
// each other's nodes for free. The portal keeps partners and settings and the
// coordinator applies them, so nothing is stored here: the local API passes
// requests on.
//
//	GET    /api/v1/sharing                 portal.SharingView: partners, invites, settings, use in the last 24h
//	POST   /api/v1/sharing/invites         {ttl_hours} → a one-person invite
//	DELETE /api/v1/sharing/invites/{id}    withdraw an invite
//	POST   /api/v1/sharing/join            {code} → the new partner
//	DELETE /api/v1/sharing/partners/{id}   stop sharing with a partner
//	PUT    /api/v1/sharing/settings        accounts.ShareSettings

// Sharing returns the signed-in user's partners, invites and settings.
func (p *PortalClient) Sharing(ctx context.Context) (*portal.SharingView, error) {
	var out portal.SharingView
	err := p.Do(ctx, http.MethodGet, "/api/v1/sharing", nil, &out)
	return &out, err
}

// NewShareInvite makes an invite maxUses people can join with (0: one);
// ttlHours 0 is the portal's default. approval: joining waits for the
// inviter's approval.
func (p *PortalClient) NewShareInvite(ctx context.Context, ttlHours float64, approval bool, maxUses int64) (*accounts.ShareInvite, error) {
	var out accounts.ShareInvite
	err := p.Do(ctx, http.MethodPost, "/api/v1/sharing/invites", map[string]any{"ttl_hours": ttlHours, "approval": approval, "max_uses": maxUses}, &out)
	return &out, err
}

// RevokeShareInvite withdraws an open invite.
func (p *PortalClient) RevokeShareInvite(ctx context.Context, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/sharing/invites/%d", id), nil, nil)
}

// JoinShare becomes the partner of whoever made code.
func (p *PortalClient) JoinShare(ctx context.Context, code string) (*accounts.SharePartner, error) {
	var out accounts.SharePartner
	err := p.Do(ctx, http.MethodPost, "/api/v1/sharing/join", map[string]string{"code": code}, &out)
	return &out, err
}

// LeaveSharePartner stops sharing with a partner.
func (p *PortalClient) LeaveSharePartner(ctx context.Context, id int64) error {
	return p.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/sharing/partners/%d", id), nil, nil)
}

// SetShareSettings replaces the signed-in user's sharing settings.
func (p *PortalClient) SetShareSettings(ctx context.Context, s accounts.ShareSettings) error {
	return p.Do(ctx, http.MethodPut, "/api/v1/sharing/settings", s, nil)
}

func (a *Agent) sharingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sharing", a.portalPassthrough("/api/v1/sharing"))
	mux.HandleFunc("POST /api/v1/sharing/invites", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/sharing/invites/{id}", a.portalForward)
	mux.HandleFunc("POST /api/v1/sharing/join", a.portalForward)
	mux.HandleFunc("DELETE /api/v1/sharing/partners/{id}", a.portalForward)
	mux.HandleFunc("PUT /api/v1/sharing/settings", a.portalForward)
	a.requestRoutes(mux, "sharing")
}
