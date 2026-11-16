package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"sharetoken/internal/relay"
	"sharetoken/internal/relaypass"
)

// A private relay is one a user runs for themselves: it admits only the
// reservations of the accounts its owner lists (access.networks.allow_accounts)
// and has no coordinator on it (no allow_peers), so it carries only what its
// owners' nodes are sent, never anyone's traffic with the coordinator. Nobody
// registers it anywhere: its owners add its address to their clients.

// keyRefreshEvery is how often a network with a portal_url asks the portal for
// its pass key. A portal that rotates the key signs with the new one only
// after announcing it for longer than this.
const keyRefreshEvery = 6 * time.Hour

// fetchPassKey asks a portal for the public key its relay passes verify with.
func fetchPassKey(ctx context.Context, portal string) (ed25519.PublicKey, error) {
	u, err := url.Parse(portal)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad portal URL %q", portal)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Host)) {
		// Whoever answers for the portal decides who may use the relay.
		return nil, fmt.Errorf("portal URL %q must be https", portal)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(portal, "/")+"/api/v1/network", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", portal, resp.Status)
	}
	var info struct {
		PassKey string `json:"pass_key"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("%s: %w", portal, err)
	}
	if info.PassKey == "" {
		return nil, fmt.Errorf("%s issues no relay passes", portal)
	}
	return relaypass.ParseKey(info.PassKey)
}

// refreshKeys keeps the pass keys of the networks that name a portal_url
// current, so that a private relay nobody maintains survives the portal
// changing its key. Keys are only ever added; a failure changes nothing.
func refreshKeys(ctx context.Context, r *relay.Relay, nets []NetworkFile) {
	var todo []NetworkFile
	for _, n := range nets {
		if n.PortalURL != "" {
			todo = append(todo, n)
		}
	}
	if len(todo) == 0 {
		return
	}
	t := time.NewTicker(keyRefreshEvery)
	defer t.Stop()
	for {
		for _, n := range todo {
			key, err := fetchPassKey(ctx, n.PortalURL)
			if err != nil {
				log.Printf("network %s: reading the pass key from the portal: %v", n.Name, err)
				continue
			}
			if added, err := r.AddPortalKey(n.Name, key); err != nil {
				log.Printf("network %s: the portal's pass key %s was not taken: %v", n.Name, relaypass.FormatKey(key), err)
			} else if added {
				log.Printf("network %s: the portal has a new pass key, %s; add it to portal_keys", n.Name, relaypass.FormatKey(key))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// initPrivate writes the configuration of a private relay.
func initPrivate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	portal := fs.String("portal", "https://sharetoken.app", "the portal whose users the relay serves")
	var accounts, announce listFlag
	fs.Var(&accounts, "account", "an account whose devices may use the relay (`user:N`, shown by `sharetoken account`); repeat or separate with commas")
	fs.Var(&announce, "announce", "public multiaddrs to advertise, e.g. /dns4/relay.example.com/tcp/4001 (needed behind 1:1 NAT)")
	out := fs.String("out", "", "write the file here instead of printing it (never overwritten)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(accounts) == 0 {
		return errors.New("name at least one -account: without one the relay would serve every user of the portal")
	}
	key, err := fetchPassKey(context.Background(), *portal)
	if err != nil {
		return fmt.Errorf("reading the portal's pass key: %w", err)
	}
	text := privateConfig(strings.TrimRight(*portal, "/"), relaypass.FormatKey(key), accounts, announce)
	var f File
	if err := checkYAML(text, &f); err != nil {
		return err
	}
	if _, err := relayConfig(f); err != nil {
		return err
	}
	if *out == "" {
		_, err := io.WriteString(stdout, text)
		return err
	}
	w, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, text); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func yamlList(indent string, items []string, empty string) string {
	if len(items) == 0 {
		return " " + empty + "\n"
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, it := range items {
		fmt.Fprintf(&b, "%s- %q\n", indent, it)
	}
	return b.String()
}

func privateConfig(portal, key string, accounts, announce []string) string {
	return `# A private sharetoken relay: it relays only to the devices of the accounts
# listed below. Start it with
#   relay -config relay.yaml
# and add the address it prints to those devices:
#   sharetoken relays add '<address>'
version: 1

identity:
  key_file: relay.key            # the relay's address includes this key: keep it

network:
  listen:
    - /ip4/0.0.0.0/tcp/4001
    - /ip4/0.0.0.0/udp/4001/quic-v1
    - /ip6/::/tcp/4001
    - /ip6/::/udp/4001/quic-v1
  # Public addresses to advertise instead of the interface addresses; needed
  # behind 1:1 NAT (most cloud hosts). /dns4 and /dns6 names work.
  announce:` + yamlList("    ", announce, "[]") + `  max_conns: 1024

access:
  networks:
    - name: sharetoken
      # Where the relay reads the key that passes are signed with, again every
      # few hours, so that it keeps working when the portal changes its key.
      portal_url: ` + portal + `
      portal_keys:
        - ` + key + `
      # Only devices of these accounts may reserve here.
      allow_accounts:` + yamlList("        ", accounts, "[]") + `      max_devices_per_user: 8

limits:
  max_reservations: 64
  max_reservations_per_ip: 16
  max_circuits: 256              # open circuits to one of the devices
  reservation_ttl: 1h
  circuit_buffer: 16KiB

status:
  listen: ""                     # e.g. 127.0.0.1:8093 for statistics and /metrics
  stats_interval: 5m

log:
  level: info
  format: text
  file: ""
`
}
