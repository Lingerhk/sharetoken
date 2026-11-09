// Command sharetoken is the user client: one program to buy and to share
// open-weight model inference on the sharetoken network.
//
//	sharetoken register -user alice     # or: login
//	sharetoken share add qwen2.5:7b                            # optional: share a local model
//	sharetoken up                                              # run; OpenAI endpoint at http://127.0.0.1:9787/v1
//	sharetoken status | balance | models | relays | jobs
//
// Settings live in config.json in the state directory: ~/.sharetoken for a prod
// build, ~/.sharetoken-dev for a dev build ($SHARETOKEN_HOME overrides).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/term"

	"sharetoken/client"
	"sharetoken/internal/cli"
	"sharetoken/internal/version"
	"sharetoken/node"
	"sharetoken/p2p"
	"sharetoken/protocol"
)

const usage = `usage: sharetoken [-home DIR] <command> [flags]

Getting started:
  register [-email ADDR | -user NAME] [-code CODE] [-portal-pin FP]
                                           create an account and bind this device; portals that verify
                                           addresses mail a code (asked for here, or give it with -code)
  login    [-user NAME] [-portal-pin FP]   log in and bind this device
  up [-http ADDR] [-listen MADDRS]      run the client (buy, and share if enabled)
  daemon [-http ADDR] [-listen MADDRS]  run the service behind the desktop app: like up, but it
                                        also runs logged out; sign-up and login go through its API

While running:
  status                                connection, relays, sharing
  models                                models on the network, nodes and prices
  relays [-measure]                     relay round-trip times; -measure re-measures now

Account (portal):
  balance                               credits and token usage
  jobs [-role buy|sell] [-n 20]         recent jobs
  logout

Sharing a local model:
  share                                 show the sharing settings
  share detect                          list the models of the local model server
  share add <model> [-id ID] [-in P] [-out P]   share a model (prices: micro-credits per million tokens)
  share rm <id>
  share on | off
  share upstream <url>                  local model server (default Ollama: http://127.0.0.1:11434/v1)

  config                                show the configuration file
  version                               show what this client was built for`

func main() {
	home := flag.String("home", client.Home(), "client state directory")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cmd, args := flag.Arg(0), flag.Args()[1:]
	if cmd == "version" { // before LoadConfig: it must not touch the state directory
		fmt.Printf("%s\napi v%d\nenv %s\nportal %s\nhome %s\nlocal api %s\n", version.String("client"), version.API, client.Env, client.PortalAddr(), *home, client.DefaultHTTP())
		return
	}
	cfg, err := client.LoadConfig(*home)
	if err != nil {
		log.Fatal(err)
	}
	c := &cmdCtx{home: *home, cfg: cfg}
	var run func([]string) error
	switch cmd {
	case "register":
		run = func(a []string) error { return c.auth(a, true) }
	case "login":
		run = func(a []string) error { return c.auth(a, false) }
	case "logout":
		run = c.logout
	case "up", "run":
		run = c.up
	case "daemon":
		run = c.runDaemon
	case "status":
		run = c.status
	case "models":
		run = c.models
	case "relays":
		run = c.relays
	case "balance", "account":
		run = c.balance
	case "jobs":
		run = c.jobs
	case "share":
		run = c.share
	case "config":
		run = c.showConfig
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type cmdCtx struct {
	home string
	cfg  client.Config
}

func (c *cmdCtx) save() error { return client.SaveConfig(c.home, c.cfg) }

func (c *cmdCtx) portal() (*client.PortalClient, error) {
	if c.cfg.Portal == "" || c.cfg.Token == "" {
		return nil, errors.New("not logged in: run `sharetoken login` (or `sharetoken register`) first")
	}
	return client.NewPortalClient(c.cfg.Portal, c.cfg.Token, c.cfg.PortalPin), nil
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// ---- account ----

func (c *cmdCtx) auth(args []string, register bool) error {
	name := "login"
	if register {
		name = "register"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	portalURL := new(string) // not a flag: the portal is built in (SHARETOKEN_PORTAL points a test network elsewhere)
	*portalURL = client.PortalAddr()
	user := fs.String("user", c.cfg.User, "username")
	var email, code *string
	if register {
		email = fs.String("email", "", "email address to sign up with")
		code = fs.String("code", "", "verification code already received by email")
	}
	device := fs.String("device", c.cfg.DeviceName, "name of this device (default: host name)")
	pin := fs.String("portal-pin", "", "fingerprint of the portal's self-signed certificate (sha256:…), as its operator published it")
	allowHTTP := fs.Bool("allow-http", false, "allow a plain-HTTP portal on another machine (test networks only)")
	_ = fs.Parse(args)
	var err error
	if register && *email != "" {
		*user = ""
	}
	if *user == "" && !(register && *email != "") {
		if *user, err = cli.Prompt("Email (or username): "); err != nil {
			return err
		}
	}
	*portalURL = strings.TrimRight(*portalURL, "/")
	cx, cancel := ctx()
	defer cancel()
	if *pin == "" && *portalURL == c.cfg.Portal {
		*pin = c.cfg.PortalPin // logging in again to the same portal
	}
	if *pin, err = trustPortal(cx, *portalURL, *pin, *allowHTTP); err != nil {
		return err
	}
	pw, err := cli.ReadPassword("Password: ", register)
	if err != nil {
		return err
	}
	if *device == "" {
		*device, _ = os.Hostname()
	}
	pc := client.NewPortalClient(*portalURL, "", *pin)
	var res *client.AuthResult
	if register && *email == "" {
		if info, ierr := pc.Info(cx); ierr == nil && info.EmailVerification {
			// This portal only takes email sign-ups: what was typed must be one.
			*email, *user = *user, ""
		}
	}
	if register && *email != "" {
		if *code == "" {
			if info, ierr := pc.Info(cx); ierr == nil && info.EmailVerification {
				if _, err := pc.RequestRegisterCode(cx, *email); err != nil {
					return err
				}
				fmt.Printf("a verification code was sent to %s\n", *email)
				if *code, err = cli.Prompt("Verification code: "); err != nil {
					return err
				}
				// The time the user took to read the mail counts against ctx().
				cancel()
				cx, cancel = ctx()
				defer cancel()
			}
		}
		res, err = pc.RegisterEmail(cx, *email, pw, strings.TrimSpace(*code), "client on "+*device)
	} else if register {
		res, err = pc.Register(cx, *user, pw, "client on "+*device)
	} else {
		res, err = pc.Login(cx, *user, pw, "client on "+*device)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.home, 0o700); err != nil {
		return err
	}
	keys, err := p2p.LoadOrCreateKeys(c.home + "/" + client.KeyFile)
	if err != nil {
		return err
	}
	pc.Token = res.Token
	if _, err := pc.BindDevice(cx, keys, res.User.Username, *device); err != nil {
		return fmt.Errorf("logged in, but binding this device failed: %w", err)
	}
	c.cfg.Portal, c.cfg.PortalPin, c.cfg.User, c.cfg.Token, c.cfg.DeviceName = *portalURL, *pin, res.User.Username, res.Token, *device
	if err := c.save(); err != nil {
		return err
	}
	fmt.Printf("logged in as %s; this device (%s) is bound to your account\n", res.User.Username, keys.ID())
	fmt.Println("next: `sharetoken up` (and `sharetoken share add <model>` to earn credits)")
	return nil
}

// trustPortal decides how to trust the portal before a password is sent to
// it, and returns the certificate pin to keep ("" when none is needed).
//   - http:// to another machine: refused unless allowHTTP.
//   - https:// with a certificate from a trusted CA: no pin.
//   - https:// with a self-signed certificate: the given pin must match;
//     without one the fingerprint is shown and the user is asked to confirm
//     it against what the operator published (trust on first use).
func trustPortal(ctx context.Context, portalURL, pin string, allowHTTP bool) (string, error) {
	if strings.HasPrefix(portalURL, "http://") {
		if client.InsecurePortalURL(portalURL) && !allowHTTP {
			return "", fmt.Errorf("refusing to send your password over plain HTTP to %s: use https:// (or -allow-http on a trusted test network)", portalURL)
		}
		return "", nil
	}
	cert, err := client.ProbePortal(ctx, portalURL)
	if err != nil {
		return "", fmt.Errorf("cannot reach the portal: %w", err)
	}
	switch {
	case pin != "":
		if !strings.EqualFold(pin, cert.Fingerprint) {
			return "", fmt.Errorf("the portal's certificate %s does not match the pin %s: someone may be intercepting the connection, or the operator replaced the certificate", cert.Fingerprint, pin)
		}
		return pin, nil
	case cert.Trusted:
		return "", nil
	}
	fmt.Fprintf(os.Stderr, "The portal uses a self-signed certificate:\n   %s\n   %s, valid until %s\n", cert.Fingerprint, cert.Subject, cert.NotAfter.Format(time.DateOnly))
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("confirm it with -portal-pin <fingerprint> (compare with the one the portal operator published)")
	}
	ans, err := cli.Prompt("Does it match the fingerprint the operator published? [y/N] ")
	if err != nil || !strings.EqualFold(strings.TrimSpace(ans), "y") {
		return "", errors.New("portal certificate not trusted")
	}
	return cert.Fingerprint, nil
}

func (c *cmdCtx) logout([]string) error {
	pc, err := c.portal()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	if err := pc.Logout(cx); err != nil && !client.IsUnauthorized(err) {
		return err
	}
	c.cfg.Token = ""
	if err := c.save(); err != nil {
		return err
	}
	fmt.Println("logged out")
	// A client running on this login has to stop too: its device key would go on
	// buying and selling. It finds out by itself within a minute; this is at once.
	if c.daemon(http.MethodPost, "/api/v1/login/check", nil, nil) == nil {
		fmt.Println("the running client is stopping")
	}
	return nil
}

func credits(micros int64) string {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	return fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000)
}

func (c *cmdCtx) balance([]string) error {
	pc, err := c.portal()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	me, err := pc.Me(cx)
	if err != nil {
		return err
	}
	b, u := me.Balance, me.Usage
	fmt.Printf("user %s (%s)\n", me.User.Username, b.Account)
	fmt.Printf("  available %s credits, on hold %s\n", credits(b.AvailableMicros), credits(b.HeldMicros))
	fmt.Printf("  bought  %d jobs, %d prompt + %d completion tokens, spent %s\n", u.Bought.Jobs, u.Bought.PromptTokens, u.Bought.CompletionTokens, credits(int64(u.Bought.Micros)))
	fmt.Printf("  sold    %d jobs, %d prompt + %d completion tokens, earned %s (pending %d)\n", u.Sold.Jobs, u.Sold.PromptTokens, u.Sold.CompletionTokens, credits(int64(u.Sold.Micros)), u.Sold.Pending)
	fmt.Printf("  devices %d\n", len(me.Devices))
	return nil
}

func (c *cmdCtx) jobs(args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	role := fs.String("role", "", "buy or sell (default both)")
	n := fs.Int("n", 20, "how many")
	_ = fs.Parse(args)
	pc, err := c.portal()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	js, err := pc.Jobs(cx, *role, *n)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tROLE\tMODEL\tSTATUS\tPROMPT\tCOMPL\tCOST\tEARNED")
	for _, j := range js {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", time.Unix(0, j.CreatedAt).Format("01-02 15:04:05"), j.Role, j.Model, j.Status,
			j.PromptTokens, j.CompletionTokens, credits(int64(j.CostMicros)), credits(int64(j.EarnedMicros)))
	}
	return tw.Flush()
}

// ---- running ----

func (c *cmdCtx) up(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	httpAddr := fs.String("http", c.cfg.HTTP, "local API / OpenAI endpoint address (not saved)")
	listen := fs.String("listen", strings.Join(c.cfg.Listen, ","), "libp2p listen addresses (not saved)")
	nat := fs.Bool("nat", c.cfg.NAT, "UPnP/NAT-PMP and hole punching (not saved)")
	_ = fs.Parse(args)
	c.cfg.HTTP, c.cfg.Listen, c.cfg.NAT = *httpAddr, cli.List(*listen), *nat
	if c.cfg.Token == "" {
		return errors.New("not logged in: run `sharetoken login` (or `sharetoken register`) first")
	}
	sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := client.NewAgent(c.home, c.cfg)
	var loginEnded atomic.Bool
	a.OnLoginEnded = func() {
		loginEnded.Store(true)
		stop()
	}
	if err := a.Start(sctx); err != nil {
		return err
	}
	st := a.Status()
	fmt.Printf("sharetoken client for %s (device %s)\n", st.User, st.Peer)
	fmt.Printf("   OpenAI-compatible endpoint: %s\n", st.API)
	if st.Coordinator.Via != "" {
		fmt.Printf("   coordinator via relay %s\n", st.Coordinator.Via)
	}
	if st.Share.Running {
		for _, m := range st.Share.Models {
			fmt.Printf("   sharing %s (upstream %s) at in=%d out=%d micro-credits/Mtok\n", m.ID, m.UpstreamModel, m.PriceInMicros, m.PriceOutMicros)
		}
	} else {
		fmt.Println("   not sharing (sharetoken share add <model> && sharetoken share on)")
	}
	for _, w := range st.Warnings {
		fmt.Println("   warning:", w)
	}
	<-sctx.Done()
	fmt.Println("stopping…")
	a.Close()
	if loginEnded.Load() {
		return errors.New("you are logged out or this login was revoked: run `sharetoken login` to continue")
	}
	return nil
}

// runDaemon serves the desktop app: the local API, its pages, and the agent
// whenever a user is logged in.
func (c *cmdCtx) runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	httpAddr := fs.String("http", c.cfg.HTTP, "local API address, on loopback (not saved)")
	listen := fs.String("listen", strings.Join(c.cfg.Listen, ","), "libp2p listen addresses (not saved)")
	nat := fs.Bool("nat", c.cfg.NAT, "UPnP/NAT-PMP and hole punching (not saved)")
	allowHTTP := fs.Bool("allow-http", false, "allow a plain-HTTP portal on another machine (test networks only)")
	stopOnEOF := fs.Bool("stop-on-stdin-eof", false, "stop in order when standard input closes (the desktop app holds it open; Windows has no SIGTERM to send)")
	_ = fs.Parse(args)
	sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *stopOnEOF {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	d := client.NewDaemon(c.home)
	d.HTTP, d.Listen, d.NAT, d.AllowHTTP = *httpAddr, cli.List(*listen), nat, *allowHTTP
	if err := d.Start(); err != nil {
		return err
	}
	fmt.Printf("sharetoken daemon: app and API at http://%s/\n", d.Addr())
	<-sctx.Done()
	fmt.Println("stopping…")
	d.Close()
	return nil
}

// daemon calls the running client's local API.
func (c *cmdCtx) daemon(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, "http://"+client.APIAddr(c.home, c.cfg)+path, rd)
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 90 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("the client is not running (start it with `sharetoken up`): %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return errors.New(e.Error.Message)
		}
		return fmt.Errorf("%s: %s", resp.Status, data)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *cmdCtx) status([]string) error {
	var st client.Status
	if err := c.daemon(http.MethodGet, "/api/v1/status", nil, &st); err != nil {
		return err
	}
	fmt.Printf("user %s, device %s\n", st.User, st.Peer)
	fmt.Printf("endpoint %s (running since %s)\n", st.API, st.Started.Format(time.DateTime))
	conn := "not connected"
	if st.Coordinator.Connected {
		conn = "connected"
		if st.Coordinator.Relayed {
			conn += " (relayed)"
		}
	}
	fmt.Printf("coordinator %s: %s via %s\n", short(st.Coordinator.Peer), conn, relayLabel(st.Coordinator.Via, st.Relays))
	printLink(st.Coordinator.Link)
	if !st.MeasuredAt.IsZero() {
		fmt.Printf("relays (measured %s ago):\n", time.Since(st.MeasuredAt).Round(time.Second))
	}
	printRelays(st.Relays)
	if st.Share.Running && st.Share.Stats != nil {
		s := st.Share.Stats
		fmt.Printf("sharing %d model(s): %d jobs served, %d failed, %d active, earned %s (acked)\n",
			len(st.Share.Models), s.JobsServed, s.JobsFailed, s.Active, credits(int64(s.EarnedMicros)))
		if s.AnnounceError != "" {
			fmt.Println("   offer not accepted:", s.AnnounceError)
		}
	} else {
		fmt.Println("not sharing")
	}
	for _, w := range st.Warnings {
		fmt.Println("warning:", w)
	}
	return nil
}

func short(id string) string {
	if len(id) > 16 {
		return id[:8] + "…" + id[len(id)-6:]
	}
	return id
}

// printLink shows the live quality of the current relay link.
func printLink(q *p2p.LinkQuality) {
	if q == nil {
		fmt.Println("   link: no ping yet")
		return
	}
	if q.AvgMillis == 0 && q.LossPct == 100 {
		fmt.Printf("   link: no reply (%d/%d pings lost) %s\n", q.Samples, q.Samples, q.LastError)
		return
	}
	fmt.Printf("   link: %.1f ms now, avg %.1f ms (min %.1f, max %.1f), jitter %.1f ms, loss %.0f%% over %d pings\n",
		q.LastMillis, q.AvgMillis, q.MinMillis, q.MaxMillis, q.JitterMillis, q.LossPct, q.Samples)
}

func printRelays(rs []client.RelayView) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tNET\tADDRESS\tRTT\tCOORD\tUSE")
	for _, r := range rs {
		rtt := r.Error
		if rtt == "" {
			rtt = fmt.Sprintf("%.1f ms", r.RTTMillis)
		} else if len(rtt) > 40 {
			rtt = rtt[:40] + "…"
		}
		use := ""
		if r.Current {
			use = "coordinator"
		}
		if r.Reserved {
			use = strings.TrimPrefix(use+",sharing", ",")
		}
		nets, addr := relaySummary(r.Addr)
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%v\t%s\n", cmpOr(r.Name, "-"), nets, addr, rtt, r.CoordinatorOK, use)
	}
	_ = tw.Flush()
}

// relaySummary condenses a relay's addresses for display: its IP families
// ("v4", "v6", "v4+v6") and the first address without the peer ID, with a
// count of the others.
func relaySummary(addr string) (nets, short string) {
	ai, err := p2p.ParseRelay(addr)
	if err != nil {
		return "?", addr
	}
	var v4, v6 bool
	for _, a := range ai.Addrs {
		_, ip6 := a.ValueForProtocol(ma.P_IP6)
		_, dns6 := a.ValueForProtocol(ma.P_DNS6)
		if ip6 == nil || dns6 == nil {
			v6 = true
		} else {
			v4 = true
		}
	}
	switch {
	case v4 && v6:
		nets = "v4+v6"
	case v6:
		nets = "v6"
	default:
		nets = "v4"
	}
	short = ai.Addrs[0].String()
	if n := len(ai.Addrs) - 1; n > 0 {
		short += fmt.Sprintf(" (+%d)", n)
	}
	return nets, short + " " + shortID(ai.ID.String())
}

// relayLabel names a relay: its registered name, else its condensed address.
func relayLabel(addr string, rs []client.RelayView) string {
	if addr == "" {
		return "-"
	}
	for _, r := range rs {
		if r.Addr == addr && r.Name != "" {
			return r.Name
		}
	}
	_, s := relaySummary(addr)
	return s
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:4] + "…" + id[len(id)-4:]
	}
	return id
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c *cmdCtx) relays(args []string) error {
	fs := flag.NewFlagSet("relays", flag.ExitOnError)
	measure := fs.Bool("measure", false, "re-measure now")
	_ = fs.Parse(args)
	if *measure {
		var rs []client.RelayView
		if err := c.daemon(http.MethodPost, "/api/v1/relays/measure", nil, &rs); err != nil {
			return err
		}
		printRelays(rs)
		return nil
	}
	var st client.Status
	if err := c.daemon(http.MethodGet, "/api/v1/status", nil, &st); err != nil {
		return err
	}
	if !st.MeasuredAt.IsZero() {
		fmt.Printf("measured %s ago (sharetoken relays -measure to re-measure)\n", time.Since(st.MeasuredAt).Round(time.Second))
	}
	printRelays(st.Relays)
	if st.Coordinator.Via != "" {
		fmt.Printf("current relay %s\n", relayLabel(st.Coordinator.Via, st.Relays))
		printLink(st.Coordinator.Link)
	}
	return nil
}

func (c *cmdCtx) models([]string) error {
	var out struct {
		Models []protocol.ModelSummary `json:"models"`
	}
	if err := c.daemon(http.MethodGet, "/api/v1/models", nil, &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tNODES\tTEE\tMIN IN/Mtok\tMIN OUT/Mtok")
	for _, m := range out.Models {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", m.Model, m.Nodes, m.AttestedNodes, m.MinPriceIn, m.MinPriceOut)
	}
	return tw.Flush()
}

// ---- sharing ----

func (c *cmdCtx) share(args []string) error {
	if len(args) == 0 {
		return c.showShare()
	}
	sc := &c.cfg.Share
	switch args[0] {
	case "detect":
		fs := flag.NewFlagSet("share detect", flag.ExitOnError)
		up := fs.String("upstream", sc.Upstream, "local model server")
		_ = fs.Parse(args[1:])
		cx, cancel := ctx()
		defer cancel()
		ms, err := client.DetectModels(cx, *up)
		if err != nil {
			return fmt.Errorf("cannot list models at %s: %w", *up, err)
		}
		for _, m := range ms {
			fmt.Println(m)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("share add", flag.ExitOnError)
		id := fs.String("id", "", "model ID on the network (default: the server's model name)")
		in := fs.Uint64("in", client.DefaultPriceInMicros, "price per million prompt tokens, micro-credits")
		out := fs.Uint64("out", client.DefaultPriceOut, "price per million completion tokens, micro-credits")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return errors.New("usage: sharetoken share add <model> [-id ID] [-in P] [-out P]")
		}
		upstreamModel := args[1]
		_ = fs.Parse(args[2:])
		if *id == "" {
			*id = upstreamModel
		}
		var kept []node.ModelConfig
		for _, m := range sc.Models {
			if m.ID != *id {
				kept = append(kept, m)
			}
		}
		sc.Models = append(kept, node.ModelConfig{ID: *id, UpstreamModel: upstreamModel, PriceInMicros: *in, PriceOutMicros: *out})
		sc.Enabled = true
	case "rm":
		if len(args) != 2 {
			return errors.New("usage: sharetoken share rm <id>")
		}
		var kept []node.ModelConfig
		for _, m := range sc.Models {
			if m.ID != args[1] {
				kept = append(kept, m)
			}
		}
		if len(kept) == len(sc.Models) {
			return fmt.Errorf("not sharing %q", args[1])
		}
		sc.Models = kept
		if len(kept) == 0 {
			sc.Enabled = false
		}
	case "on":
		sc.Enabled = true
	case "off":
		sc.Enabled = false
	case "upstream":
		if len(args) != 2 {
			return errors.New("usage: sharetoken share upstream <url>")
		}
		sc.Upstream = strings.TrimRight(args[1], "/")
	default:
		return fmt.Errorf("unknown share command %q", args[0])
	}
	if err := sc.Validate(); err != nil {
		return err
	}
	if err := c.save(); err != nil {
		return err
	}
	// Apply to a running client, if any.
	if err := c.daemon(http.MethodPost, "/api/v1/share/reload", nil, nil); err != nil && !strings.Contains(err.Error(), "not running") {
		return fmt.Errorf("saved, but the running client refused it: %w", err)
	}
	return c.showShare()
}

func (c *cmdCtx) showShare() error {
	sc := c.cfg.Share
	state := "off"
	if sc.Enabled {
		state = "on"
	}
	fmt.Printf("sharing %s, upstream %s, concurrency %d, relays %d\n", state, sc.Upstream, sc.Concurrency, sc.Relays)
	for _, m := range sc.Models {
		fmt.Printf("   %s (upstream %s) in=%d out=%d micro-credits/Mtok\n", m.ID, m.UpstreamModel, m.PriceInMicros, m.PriceOutMicros)
	}
	if len(sc.Models) == 0 {
		fmt.Println("   no models: `sharetoken share detect`, then `sharetoken share add <model>`")
	}
	return nil
}

func (c *cmdCtx) showConfig([]string) error {
	cfg := c.cfg
	if cfg.Token != "" {
		cfg.Token = cfg.Token[:min(9, len(cfg.Token))] + "…"
	}
	fmt.Println("#", c.home+"/"+client.ConfigFile)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg)
}
