package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"

	"sharetoken/internal/secfile"
)

// A tailcat address names the DERP region its server waits in, with the
// region's hosts written out. A buyer does not take those hosts from the
// seller: the address would then send this machine to any host and port a
// seller cared to write (a DERP handshake is an HTTPS request, and the STUN
// probes that go with it are UDP packets). The buyer looks the region up in
// the DERP map it trusts itself, by host name, and uses that map's entry;
// a region the map does not hold is refused, and the job stays on the paths
// it has without the tailnet.

// derpMaps hands out the DERP map this client trusts: a fixed one, or the
// one behind a URL, fetched through tailcat's cache.
type derpMaps struct {
	url    string           // "" with fixed unset: tailcat's default
	fixed  *tailcfg.DERPMap // a map given outright (a file, a test)
	cache  tailcat.DERPMapCache
	mu     sync.Mutex
	last   *tailcfg.DERPMap // the latest good answer, used when a fetch fails
	lastAt time.Time
}

// newDERPMaps reads src: empty (tailcat's public relays), an http(s) URL of
// a DERP map, or the path of a file holding one. dir, when set, keeps the
// fetched map across restarts.
func newDERPMaps(src, dir string) (*derpMaps, error) {
	d := &derpMaps{}
	if dir != "" {
		d.cache = &fileCache{path: filepath.Join(dir, derpCacheFile)}
	}
	switch {
	case src == "":
	case strings.HasPrefix(src, "https://"), strings.HasPrefix(src, "http://"):
		d.url = src
	default:
		data, err := os.ReadFile(strings.TrimPrefix(src, "file://"))
		if err != nil {
			return nil, fmt.Errorf("DERP map: %w", err)
		}
		dm := new(tailcfg.DERPMap)
		if err := json.Unmarshal(data, dm); err != nil {
			return nil, fmt.Errorf("DERP map %s: %w", src, err)
		}
		if len(dm.Regions) == 0 {
			return nil, fmt.Errorf("DERP map %s names no region", src)
		}
		d.fixed = dm
	}
	return d, nil
}

// source says where the map comes from, for the status.
func (d *derpMaps) source() string {
	switch {
	case d.fixed != nil:
		return "file"
	case d.url != "":
		return d.url
	}
	return tailcat.DefaultDERPMapURL
}

// get returns the map. A fetch that fails falls back on the last map this
// process saw (tailcat's cache already falls back on the one on disk).
func (d *derpMaps) get(ctx context.Context) (*tailcfg.DERPMap, error) {
	if d.fixed != nil {
		return d.fixed, nil
	}
	d.mu.Lock()
	last, at := d.last, d.lastAt
	d.mu.Unlock()
	if last != nil && time.Since(at) < 10*time.Minute {
		return last, nil
	}
	var opts []any
	if d.url != "" {
		opts = append(opts, tailcat.DERPMapURL(d.url))
	}
	if d.cache != nil {
		opts = append(opts, d.cache)
	}
	dm, err := tailcat.FetchDERPMap(ctx, opts...)
	if err != nil {
		if last != nil {
			return last, nil
		}
		return nil, err
	}
	d.mu.Lock()
	d.last, d.lastAt = dm, time.Now()
	d.mu.Unlock()
	return dm, nil
}

// errUnknownRegion: the address names a relay region this client's map
// does not hold.
var errUnknownRegion = errors.New("the address names a relay region outside this client's DERP map")

// trusted rewrites a parsed address so that its relay region is this
// client's own description of it.
func (d *derpMaps) trusted(ctx context.Context, ci *tailcat.ConnInfo) error {
	dm, err := d.get(ctx)
	if err != nil {
		return fmt.Errorf("DERP map: %w", err)
	}
	var region *tailcfg.DERPRegion
	switch {
	case len(ci.Region) > 0:
		region = regionByHosts(dm, ci.Region[0])
	case ci.RegionID > 0:
		region = dm.Regions[ci.RegionID]
	}
	if region == nil {
		return errUnknownRegion
	}
	ci.Region, ci.RegionID = []*tailcfg.DERPRegion{relayNodes(region)}, 0
	return nil
}

// regionByHosts finds the region of dm that has one of want's relay hosts.
func regionByHosts(dm *tailcfg.DERPMap, want *tailcfg.DERPRegion) *tailcfg.DERPRegion {
	if want == nil {
		return nil
	}
	hosts := map[string]bool{}
	for _, n := range want.Nodes {
		if n != nil && n.HostName != "" {
			hosts[strings.ToLower(n.HostName)] = true
		}
	}
	for _, r := range dm.Regions {
		for _, n := range r.Nodes {
			if n != nil && !n.STUNOnly && hosts[strings.ToLower(n.HostName)] {
				return r
			}
		}
	}
	return nil
}

// relayNodes is r with at most two relay nodes, as tailcat writes a region
// into an address (STUN-only nodes carry no traffic).
func relayNodes(r *tailcfg.DERPRegion) *tailcfg.DERPRegion {
	out := *r
	out.Nodes = nil
	for _, n := range r.Nodes {
		if n == nil || n.STUNOnly {
			continue
		}
		out.Nodes = append(out.Nodes, n)
		if len(out.Nodes) == 2 {
			break
		}
	}
	return &out
}

// ---- the map on disk ----

const derpCacheFile = "derpmap.json"

// fileCache keeps the fetched DERP map in the client's home, so that a
// start without the map's server still finds its relays.
type fileCache struct {
	path string
	mu   sync.Mutex
}

type cachedMap struct {
	URL      string          `json:"url"`
	ETag     string          `json:"etag,omitempty"`
	StoredAt time.Time       `json:"stored_at"`
	Data     json.RawMessage `json:"data"`
}

func (c *fileCache) Get(url string) (data []byte, etag string, storedAt time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return nil, "", time.Time{}, false
	}
	var m cachedMap
	if json.Unmarshal(raw, &m) != nil || m.URL != url || len(m.Data) == 0 {
		return nil, "", time.Time{}, false
	}
	return m.Data, m.ETag, m.StoredAt, true
}

func (c *fileCache) Put(url string, data []byte, etag string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !json.Valid(data) {
		return errors.New("DERP map is not JSON")
	}
	raw, err := json.Marshal(cachedMap{URL: url, ETag: etag, StoredAt: time.Now().UTC(), Data: data})
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := secfile.WriteFile(tmp, raw); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
