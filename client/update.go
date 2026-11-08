package client

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"sharetoken/internal/version"
	"sharetoken/portal"
)

// userAgent says what this client is. The portal logs it; decisions use the
// version header.
func userAgent() string {
	return version.UserAgent("client") + " (" + Env + "; " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// Update tells the app whether a newer client exists.
type Update struct {
	Available   bool   `json:"available"`        // a newer release is recommended
	Required    bool   `json:"required"`         // this version is below the network's minimum
	Latest      string `json:"latest,omitempty"` // the newest release
	DownloadURL string `json:"download_url,omitempty"`
}

// updateFor compares a build against what the portal reports. A build without
// a version number (not stamped) has nothing to compare.
func updateFor(current string, c portal.ClientVersions) Update {
	u := Update{Latest: c.Latest, DownloadURL: c.DownloadURL}
	if !version.Valid(current) {
		return u
	}
	core := version.Core(current)
	older := func(than string) bool { return than != "" && version.Valid(than) && version.Compare(core, than) < 0 }
	u.Required = older(c.Min)
	u.Available = u.Required || older(c.Recommended) || older(c.Latest)
	return u
}

// Version asks the portal what it runs and which clients it wants.
func (p *PortalClient) Version(ctx context.Context) (*portal.VersionInfo, error) {
	var out portal.VersionInfo
	err := p.Do(ctx, http.MethodGet, "/api/v1/version", nil, &out)
	return &out, err
}

// updateCheckEvery is how often a running client looks for a new release.
const updateCheckEvery = time.Hour

// watchUpdates keeps the session's update information current until ctx ends.
func (d *Daemon) watchUpdates(ctx context.Context) {
	for {
		d.checkUpdate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateCheckEvery):
		}
	}
}

func (d *Daemon) checkUpdate(ctx context.Context) {
	d.mu.Lock()
	base, pin := d.cfg.Portal, d.cfg.PortalPin
	d.mu.Unlock()
	if base == "" {
		base = PortalAddr()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	info, err := NewPortalClient(base, "", pin).Version(ctx)
	if err != nil {
		d.log.Debug("checking for a new release", "err", err)
		return
	}
	u := updateFor(version.Version, info.Client)
	d.mu.Lock()
	d.update = u
	d.mu.Unlock()
}
