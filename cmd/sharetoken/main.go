// Command sharetoken is the user client: one program to buy and to share
// open-weight model inference on the sharetoken network.
//
//	sharetoken login -email alice@example.com    # a code is mailed; a new address gets an account
//	sharetoken share add qwen2.5:7b                            # optional: share a local model
//	sharetoken provider add deepseek                           # or a hosted API, with your own API key
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
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/term"

	"sharetoken/internal/accounts"
	"sharetoken/internal/agentskill"
	"sharetoken/internal/buyer"
	"sharetoken/internal/cli"
	"sharetoken/internal/client"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/provider"
	"sharetoken/internal/version"
)

const usage = `usage: sharetoken [-home DIR] <command> [flags]

Getting started:
  login [-email ADDR] [-code CODE] [-invite CODE] [-totp CODE] [-device NAME] [-portal-pin FP]
                                        sign in with a code mailed to ADDR and bind this device; an
                                        address without an account gets one. -send only mails the code,
                                        -code only redeems one (for scripts: two steps)
  up [-http ADDR] [-listen MADDRS]      run the client (buy, and share if enabled)
  daemon [-http ADDR] [-listen MADDRS]  run the service behind the desktop app: like up, but it
                                        also runs logged out; signing in goes through its API

While running:
  status                                connection, relays, sharing
  models                                models on the network, nodes and prices
  relays [-measure]                     relay round-trip times; -measure re-measures now
  relays add <address>                  share through a relay of your own (a private relay)
  relays remove <address|peer id>       stop using it
  tailnet                               the direct path over tunnels: whether it is on, this node's relay region,
                                        and each tunnel's path (direct, or through which DERP relay)
  tailnet on [-derp-map URL|FILE] [-wait MS] [-transport quic|tcp]
                                        reach sellers, and be reached as a seller, through WireGuard tunnels that
                                        go direct where the NATs allow (Tailscale's data plane; no account, no
                                        system VPN). Both ends need it on. -derp-map: the relays tunnels start
                                        through (default: tailcat's public ones); -wait: how long a request waits
                                        for a new tunnel before it uses the relay (default 0); -transport: what
                                        a connection through a tunnel runs on (default quic)
  tailnet off                           back to the relays and libp2p's own direct connections

Account (portal):
  balance                               credits and token usage
  jobs [-role buy|sell] [-n 20]         recent jobs
  logout

Sharing a model:
  share                                 show the sharing settings
  share detect                          list the models of the local model server
  share add <model> [-provider NAME] [-id ID] [-in P] [-out P] [-max-output N]
                                        share a model of the local model server, or of a provider
                                        (prices: micro-credits per million tokens)
  share rm <id>
  share on | off
  share upstream <url>                  local model server (default Ollama: http://127.0.0.1:11434/v1)

Hosted APIs to share (kept in providers.json, apart from the configuration):
  provider                              the kinds that can be added, and those configured
  provider add <kind> [-name N] [-url URL] [-header K=V] [-no-check]
                                        add one with an API key (asked for; or stdin, or $SHARETOKEN_PROVIDER_KEY):
                                        anthropic, openai, gemini, grok, deepseek, or custom with -url
  provider login <kind> [-name N] [-project ID]
                                        add one with an OAuth login: anthropic, openai, gemini, antigravity, grok
  provider models <name>                models its credentials may use
  provider configure <name> [-proxy-mode environment|direct|custom] [-proxy URL]
                           [-profile standard|claude-code] [-sampling strict|default]
                           [-tls-profile native|chrome] [-token-tls-profile native|chrome]
  provider login anthropic [-mode full|inference] [-session-key] [-profile standard|claude-code]
  provider test <name> [-model M]       send one small request through it
  provider on | off <name>              sell its models, or stop
  provider warmup <name> [-times 06:00,11:05] [-on-reset off|week|all] [-model M] [-off]
                                        a Claude or ChatGPT login: start its usage windows at times of day,
                                        or as they reset, with one tiny request (see warmup)
  provider rm <name>

Pairing (a buyer that pairs with a seller buys only from the sellers it is paired with):
  pair                                  who you are paired with, and your pairing codes
  pair code [-label L] [-ttl 24h] [-uses N] [-approve]
                                        seller: make a code and its link to hand to your buyers (any number of them,
                                        or -uses N); -approve: each pairing waits for you to approve it
  pair join <code|link>                 buyer: pair with the seller that made the code or link
  pair approve | reject <request id>    seller: approve or reject a buyer waiting for your approval
  pair withdraw <request id>            buyer: withdraw your own request
  pair leave <seller id>                buyer: end the pairing with a seller
  pair rm <buyer id>                    seller: remove one of your buyers
  pair revoke <code id>                 seller: stop a code from admitting more buyers
  pair private on|off                   seller: serve only the buyers paired with you, or anyone

Share partners (two users use each other's nodes for free, before the market):
  partner                               your partners, open invites, settings and the tokens shared in the last 24 hours
  partner invite [-ttl 24h] [-approve]  make an invite and its link for one person (at most 168h);
                                        -approve: joining waits for you to approve it
  partner join <code|link>              become the partner of whoever made the invite
  partner approve | reject <request id> approve or reject someone waiting for your approval
  partner withdraw <request id>         withdraw your own request
  accept <link|code>                    accept an invite link (https://<portal>/p#… or /s#…) or code of either kind
  partner rm <user id>                  stop sharing with a partner
  partner revoke <invite id>            withdraw an open invite
  partner set [-only on|off] [-limit N] [-share P]
                                        -only: use partners' nodes only, never the market;
                                        -limit: tokens your nodes give all partners per 24 hours (0: no limit);
                                        -share: percent of each node's concurrent jobs partners may take (0 pauses)

The OpenAI-compatible endpoint's API key (by default required; tools send it as their OpenAI API key):
  apikey [-q]                           the endpoint, whether the key is required, and the key; -q prints the key only
  apikey on | off                       require the key, or let any program of this machine in without it
  apikey rotate                         make a new key; the old one stops working

Coding agents (Claude Code, Codex):
  skill                                 which agents are found, and whether they have the skill
  skill install [-agent claude|codex|all] [-force]
                                        teach the agents to use this client: writes skills/sharetoken/SKILL.md
                                        under ~/.claude and ~/.codex ($CLAUDE_CONFIG_DIR, $CODEX_HOME);
                                        default: every agent found. Run it again after an update
  skill uninstall [-agent …] [-force]   remove it again
  skill show                            print the skill

Warm-up (start a Claude or ChatGPT 5-hour window early, so it resets while you work):
  warmup                                every account there is to warm up, its times and what was done
  warmup set claude|codex [-times 06:00,11:05] [-on-reset off|week|all] [-model M] [-binary PATH] [-off]
                                        the Claude Code or Codex of this machine, through its own CLI
  warmup run [claude|codex|provider:<name>]
                                        warm up now (default: every one that is set)
  warmup pause | resume                 stop every automatic warm-up, or go on

  config                                show the configuration file
  version [--check]                     show what this client was built for; --check asks the portal for a newer release
  upgrade [-y]                          install the newest release: fetches its package, checks the release signature,
                                        and installs it the way this copy was installed (archive, app, installer, .deb)`

func main() {
	home := flag.String("home", client.Home(), "client state directory")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cmd, args := flag.Arg(0), flag.Args()[1:]
	removeReplacedProgram()
	if cmd == "version" { // before LoadConfig: it must not touch the state directory
		fmt.Printf("%s\napi v%d\nenv %s\nportal %s\nhome %s\nlocal api %s\n", version.String("client"), version.API, client.Env, client.PortalAddr(), *home, client.DefaultHTTP())
		if len(args) > 0 && (args[0] == "--check" || args[0] == "-check") {
			if err := checkVersion(*home); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		}
		return
	}
	cfg, err := client.LoadConfig(*home)
	if err != nil {
		log.Fatal(err)
	}
	c := &cmdCtx{home: *home, cfg: cfg}
	var run func([]string) error
	switch cmd {
	case "login":
		run = c.login
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
	case "tailnet":
		run = c.tailnet
	case "relays":
		run = c.relays
	case "balance", "account":
		run = c.balance
	case "jobs":
		run = c.jobs
	case "share":
		run = c.share
	case "provider":
		run = c.provider
	case "pair":
		run = c.pair
	case "partner":
		run = c.partner
	case "accept":
		run = c.accept
	case "apikey":
		run = c.apikey
	case "skill":
		run = c.skill
	case "warmup":
		run = c.warmup
	case "config":
		run = c.showConfig
	case "upgrade":
		run = c.upgrade
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
		return nil, errors.New("not logged in: run `sharetoken login` first")
	}
	return client.NewPortalClient(c.cfg.Portal, c.cfg.Token, c.cfg.PortalPin), nil
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// ---- account ----

func (c *cmdCtx) login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	portalURL := client.PortalAddr() // not a flag: the portal is built in (SHARETOKEN_PORTAL points a test network elsewhere)
	last := ""
	if strings.Contains(c.cfg.User, "@") {
		last = c.cfg.User
	}
	email := fs.String("email", last, "your email address: a sign-in code is mailed there")
	code := fs.String("code", "", "the code from the email: only redeem it (after -send)")
	send := fs.Bool("send", false, "only mail the code, and exit")
	invite := fs.String("invite", "", "invite code, for a new account on portals that require one")
	totpCode := fs.String("totp", "", "code from your authenticator app, for accounts with two-factor authentication")
	device := fs.String("device", c.cfg.DeviceName, "name of this device (default: host name)")
	pin := fs.String("portal-pin", "", "fingerprint of the portal's self-signed certificate (sha256:…), as its operator published it")
	allowHTTP := fs.Bool("allow-http", false, "allow a plain-HTTP portal on another machine (test networks only)")
	_ = fs.Parse(args)
	if *send && *code != "" {
		return errors.New("-send mails a code and -code redeems one: give one of them")
	}
	var err error
	if *email == "" {
		if *email, err = cli.Prompt("Email: "); err != nil {
			return err
		}
	}
	*email = strings.TrimSpace(*email)
	portalURL = strings.TrimRight(portalURL, "/")
	cx, cancel := ctx()
	defer cancel()
	if *pin == "" && portalURL == c.cfg.Portal {
		*pin = c.cfg.PortalPin // signing in again to the same portal
	}
	if *pin, err = trustPortal(cx, portalURL, *pin, *allowHTTP); err != nil {
		return err
	}
	pc := client.NewPortalClient(portalURL, "", *pin)
	if *code == "" {
		sent, err := pc.RequestCode(cx, *email, *invite)
		var pe *client.PortalError
		if errors.As(err, &pe) && pe.Code == "invite_required" && !*send {
			if *invite, err = cli.Prompt("This address has no account yet, and new accounts need an invite code: "); err != nil {
				return err
			}
			sent, err = pc.RequestCode(cx, *email, *invite)
		}
		if err != nil {
			return err
		}
		if sent.NewAccount {
			fmt.Printf("%s has no account yet: signing in creates one\n", *email)
		}
		fmt.Printf("a sign-in code was sent to %s (valid %d minutes)\n", *email, sent.ExpiresInSeconds/60)
		if *send {
			return nil
		}
		if *code, err = cli.Prompt("Code from the email: "); err != nil {
			return err
		}
		// The time the user took to read the mail counts against ctx().
		cancel()
		cx, cancel = ctx()
		defer cancel()
	}
	if *device == "" {
		*device, _ = os.Hostname()
	}
	tokenName := "client on " + *device
	res, err := pc.SignIn(cx, *email, strings.TrimSpace(*code), strings.TrimSpace(*invite), strings.TrimSpace(*totpCode), tokenName)
	var pe *client.PortalError
	switch {
	case errors.As(err, &pe) && pe.Code == "mfa_required" && pe.Challenge != "":
		second, perr := cli.Prompt("Code from your authenticator app: ")
		if perr != nil {
			return perr
		}
		res, err = pc.CompleteMFA(cx, pe.Challenge, strings.TrimSpace(second), tokenName)
	case errors.As(err, &pe) && pe.Code == "mfa_setup_required":
		return errors.New("administrators must set up two-factor authentication: sign in on the web portal first")
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
		return fmt.Errorf("signed in, but binding this device failed: %w", err)
	}
	c.cfg.Portal, c.cfg.PortalPin, c.cfg.User, c.cfg.Token, c.cfg.DeviceName = portalURL, *pin, res.User.Username, res.Token, *device
	if err := c.save(); err != nil {
		return err
	}
	if res.Created {
		fmt.Printf("account %s created\n", res.User.Username)
	}
	fmt.Printf("signed in as %s; this device (%s) is bound to your account\n", res.User.Username, keys.ID())
	fmt.Println("next: `sharetoken up` (and `sharetoken share add <model>` to earn credits)")
	return nil
}

// trustPortal decides how to trust the portal before an address or code is sent to
// it, and returns the certificate pin to keep ("" when none is needed).
//   - http:// to another machine: refused unless allowHTTP.
//   - https:// with a certificate from a trusted CA: no pin.
//   - https:// with a self-signed certificate: the given pin must match;
//     without one the fingerprint is shown and the user is asked to confirm
//     it against what the operator published (trust on first use).
func trustPortal(ctx context.Context, portalURL, pin string, allowHTTP bool) (string, error) {
	if strings.HasPrefix(portalURL, "http://") {
		if client.InsecurePortalURL(portalURL) && !allowHTTP {
			return "", fmt.Errorf("refusing to send your sign-in code over plain HTTP to %s: use https:// (or -allow-http on a trusted test network)", portalURL)
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

// ---- pairing ----

func (c *cmdCtx) pair(args []string) error {
	pc, err := c.portal()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	id := func(what string) (int64, error) {
		if len(args) != 1 {
			return 0, fmt.Errorf("usage: sharetoken pair %s <%s> (`sharetoken pair` lists them)", sub, what)
		}
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not an id: `sharetoken pair` lists them", args[0])
		}
		return n, nil
	}
	switch sub {
	case "", "ls", "list":
		return showPairing(cx, pc, c.cfg.Portal)
	case "code", "new":
		fs := flag.NewFlagSet("pair code", flag.ExitOnError)
		label := fs.String("label", "", "a note for yourself: who the code is for")
		ttl := fs.Duration("ttl", 0, "how long the code can be used (default 24h, at most 168h)")
		uses := fs.Int64("uses", 0, "how many buyers it admits (default: any number)")
		approve := fs.Bool("approve", false, "each pairing waits for you to approve it")
		_ = fs.Parse(args)
		code, err := pc.NewPairingCode(cx, *label, ttl.Hours(), *uses, *approve)
		if err != nil {
			return err
		}
		fmt.Printf("pairing code  %s\n", code.Code)
		fmt.Printf("link          %s\n", accounts.InviteLink(c.cfg.Portal, accounts.KindPair, code.LinkToken))
		fmt.Printf("  good until %s, for %s\n", time.Unix(0, code.ExpiresAt).Format("2006-01-02 15:04"), usesText(code.MaxUses, 0))
		fmt.Println("  send the link (it works before the app is installed), or a buyer runs: sharetoken pair join " + code.Code)
		if code.Approval {
			fmt.Println("  each buyer waits for your approval: `sharetoken pair` lists them, `sharetoken pair approve <id>` lets one in")
		}
		fmt.Println("  from then on that buyer's requests go only to the sellers it is paired with")
		return nil
	case "join", "redeem":
		if len(args) != 1 {
			return errors.New("usage: sharetoken pair join <code|link>")
		}
		seller, err := pc.RedeemPairingCode(cx, args[0])
		if err != nil {
			return err
		}
		if seller.Pending {
			fmt.Printf("asked seller %s (id %d) to pair: it waits for their approval\n", seller.Username, seller.ID)
			fmt.Println("  `sharetoken pair` shows it; you are paired once they approve")
			return nil
		}
		fmt.Printf("paired with seller %s (id %d)\n", seller.Username, seller.ID)
		fmt.Println("  this account now buys only from the sellers it is paired with; `sharetoken pair leave " + fmt.Sprint(seller.ID) + "` ends it")
		return nil
	case "leave":
		n, err := id("seller id")
		if err != nil {
			return err
		}
		if err := pc.LeaveSeller(cx, n); err != nil {
			return err
		}
		fmt.Println("left seller", n)
		return nil
	case "rm", "remove":
		n, err := id("buyer id")
		if err != nil {
			return err
		}
		if err := pc.RemoveBuyer(cx, n); err != nil {
			return err
		}
		fmt.Println("removed buyer", n)
		return nil
	case "revoke":
		n, err := id("code id")
		if err != nil {
			return err
		}
		if err := pc.RevokePairingCode(cx, n); err != nil {
			return err
		}
		fmt.Println("revoked code", n, "(the buyers already paired with it stay)")
		return nil
	case "approve", "reject", "withdraw":
		n, err := id("request id")
		if err != nil {
			return err
		}
		return requestAction(cx, pc, "pairing", sub, n)
	case "private":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			return errors.New("usage: sharetoken pair private on|off")
		}
		if err := pc.SetPrivateSeller(cx, args[0] == "on"); err != nil {
			return err
		}
		if args[0] == "on" {
			fmt.Println("private mode on: only the buyers paired with you are matched to you and see your models")
		} else {
			fmt.Println("private mode off: any buyer can buy from you")
		}
		return nil
	}
	return fmt.Errorf("unknown: pair %s (pair | pair code | pair join <code|link> | pair leave <id> | pair rm <id> | pair revoke <id> | pair approve|reject|withdraw <id> | pair private on|off)", sub)
}

// requestAction approves, rejects or withdraws a request for approval; area
// is "pairing" or "sharing".
func requestAction(cx context.Context, pc *client.PortalClient, area, action string, id int64) error {
	switch action {
	case "approve":
		r, err := pc.ApproveRequest(cx, area, id)
		if err != nil {
			return err
		}
		if area == "pairing" {
			fmt.Printf("approved: %s is now paired with you as a buyer\n", r.Username)
		} else {
			fmt.Printf("approved: %s is now your share partner\n", r.Username)
		}
		return nil
	case "reject":
		if err := pc.RejectRequest(cx, area, id); err != nil {
			return err
		}
		fmt.Println("rejected request", id)
		return nil
	}
	if err := pc.WithdrawRequest(cx, area, id); err != nil {
		return err
	}
	fmt.Println("withdrew request", id)
	return nil
}

// printRequests lists requests waiting for the user's approval and the user's
// own waiting for someone else's.
func printRequests(requests, waiting []accounts.InviteRequest, cmd string) {
	day := func(ns int64) string { return time.Unix(0, ns).Format("2006-01-02 15:04") }
	if len(requests) > 0 {
		fmt.Printf("\nwaiting for your approval (sharetoken %s approve|reject <id>):\n", cmd)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tACCOUNT\tASKED\tLABEL")
		for _, r := range requests {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", r.ID, r.Username, day(r.CreatedAt), r.Label)
		}
		_ = tw.Flush()
	}
	if len(waiting) > 0 {
		fmt.Printf("\nyour requests waiting for approval (sharetoken %s withdraw <id>):\n", cmd)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tOF\tASKED")
		for _, r := range waiting {
			fmt.Fprintf(tw, "  %d\t%s\t%s\n", r.ID, r.Username, day(r.CreatedAt))
		}
		_ = tw.Flush()
	}
}

// accept takes an invite link or code of either kind.
func (c *cmdCtx) accept(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sharetoken accept <link|code>")
	}
	pc, err := c.portal()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	if ref := accounts.ParseInvite(args[0]); ref.Host != "" && !strings.EqualFold(ref.Host, hostOfURL(c.cfg.Portal)) {
		return fmt.Errorf("this link is from %s, but this client uses %s", ref.Host, c.cfg.Portal)
	}
	r, err := pc.AcceptInvite(cx, args[0])
	if err != nil {
		return err
	}
	switch {
	case r.Pending:
		fmt.Printf("asked %s (id %d): it waits for their approval\n", r.User, r.ID)
	case r.Kind == accounts.KindShare:
		fmt.Printf("now sharing with %s (id %d): each of you uses the other's nodes first, for free\n", r.User, r.ID)
	default:
		fmt.Printf("paired with seller %s (id %d): this account now buys only from the sellers it is paired with\n", r.User, r.ID)
	}
	return nil
}

func hostOfURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(u.Host, ":443"), ":80")
}

func usesText(maxUses, used int64) string {
	if maxUses == 0 {
		return fmt.Sprintf("any number of buyers (%d so far)", used)
	}
	return fmt.Sprintf("%d of %d buyers", used, maxUses)
}

func showPairing(cx context.Context, pc *client.PortalClient, base string) error {
	v, err := pc.Pairing(cx)
	if err != nil {
		return err
	}
	day := func(ns int64) string { return time.Unix(0, ns).Format("2006-01-02 15:04") }
	if len(v.Sellers) == 0 {
		fmt.Println("buying: not paired, requests go to any seller")
	} else {
		fmt.Println("buying: only from these sellers")
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tSELLER\tSINCE")
		for _, s := range v.Sellers {
			fmt.Fprintf(tw, "  %d\t%s\t%s\n", s.ID, s.Username, day(s.Since))
		}
		_ = tw.Flush()
	}
	fmt.Println()
	open := "anyone else can buy too"
	if v.Private {
		open = "private mode: nobody else can buy"
	}
	if len(v.Buyers) == 0 {
		fmt.Printf("selling: no paired buyers (%s)\n", open)
	} else {
		fmt.Printf("selling: %d paired buyers (%s)\n", len(v.Buyers), open)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tBUYER\tSINCE\tCODE")
		for _, b := range v.Buyers {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", b.ID, b.Username, day(b.Since), b.Label)
		}
		_ = tw.Flush()
	}
	if len(v.Codes) > 0 {
		fmt.Println("\npairing codes:")
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tCODE\tGOOD UNTIL\tUSED BY\tLABEL")
		for _, c := range v.Codes {
			label := c.Label
			if c.Approval {
				label = strings.TrimSpace(label + " (approval)")
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", c.ID, c.Code, day(c.ExpiresAt), usesText(c.MaxUses, c.Used), label)
			if c.LinkToken != "" {
				fmt.Fprintf(tw, "  \t%s\t\t\t\n", accounts.InviteLink(base, accounts.KindPair, c.LinkToken))
			}
		}
		_ = tw.Flush()
	} else {
		fmt.Println("\nno pairing codes; `sharetoken pair code` makes one")
	}
	printRequests(v.Requests, v.Waiting, "pair")
	return nil
}

// ---- share partners ----

func (c *cmdCtx) partner(args []string) error {
	pc, err := c.portal()
	if err != nil {
		return err
	}
	cx, cancel := ctx()
	defer cancel()
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	id := func(what string) (int64, error) {
		if len(args) != 1 {
			return 0, fmt.Errorf("usage: sharetoken partner %s <%s> (`sharetoken partner` lists them)", sub, what)
		}
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not an id: `sharetoken partner` lists them", args[0])
		}
		return n, nil
	}
	switch sub {
	case "", "ls", "list":
		return showPartners(cx, pc, c.cfg.Portal)
	case "invite", "new":
		fs := flag.NewFlagSet("partner invite", flag.ExitOnError)
		ttl := fs.Duration("ttl", 0, "how long the invite can be used (default 24h, at most 168h)")
		approve := fs.Bool("approve", false, "joining waits for you to approve it")
		_ = fs.Parse(args)
		inv, err := pc.NewShareInvite(cx, ttl.Hours(), *approve)
		if err != nil {
			return err
		}
		fmt.Printf("share invite  %s\n", inv.Code)
		fmt.Printf("link          %s\n", accounts.InviteLink(c.cfg.Portal, accounts.KindShare, inv.LinkToken))
		fmt.Printf("  good until %s, for one person\n", time.Unix(0, inv.ExpiresAt).Format("2006-01-02 15:04"))
		fmt.Println("  send the link (it works before the app is installed), or they run: sharetoken partner join " + inv.Code)
		if inv.Approval {
			fmt.Println("  joining waits for your approval: `sharetoken partner` lists it, `sharetoken partner approve <id>` lets them in")
		}
		fmt.Println("  from then on each of you uses the other's nodes first, for free")
		return nil
	case "join":
		if len(args) != 1 {
			return errors.New("usage: sharetoken partner join <code|link>")
		}
		p, err := pc.JoinShare(cx, args[0])
		if err != nil {
			return err
		}
		if p.Pending {
			fmt.Printf("asked %s (id %d) to share: it waits for their approval\n", p.Username, p.ID)
			return nil
		}
		fmt.Printf("now sharing with %s (id %d)\n", p.Username, p.ID)
		fmt.Println("  each of you uses the other's nodes first, for free; `sharetoken partner rm " + fmt.Sprint(p.ID) + "` ends it")
		return nil
	case "rm", "remove", "leave":
		n, err := id("user id")
		if err != nil {
			return err
		}
		if err := pc.LeaveSharePartner(cx, n); err != nil {
			return err
		}
		fmt.Println("stopped sharing with", n)
		return nil
	case "revoke":
		n, err := id("invite id")
		if err != nil {
			return err
		}
		if err := pc.RevokeShareInvite(cx, n); err != nil {
			return err
		}
		fmt.Println("withdrew invite", n)
		return nil
	case "approve", "reject", "withdraw":
		n, err := id("request id")
		if err != nil {
			return err
		}
		return requestAction(cx, pc, "sharing", sub, n)
	case "set":
		v, err := pc.Sharing(cx)
		if err != nil {
			return err
		}
		st := v.Settings
		fs := flag.NewFlagSet("partner set", flag.ExitOnError)
		only := fs.String("only", "", "on: use partners' nodes only; off: fall back to the market")
		limit := fs.Int64("limit", st.DailyLimitTokens, "tokens your nodes give all partners per 24 hours (0: no limit)")
		share := fs.Int64("share", st.SharePct, "percent of each node's concurrent jobs partners may take (0 pauses sharing)")
		_ = fs.Parse(args)
		switch *only {
		case "on":
			st.OnlyPartners = true
		case "off":
			st.OnlyPartners = false
		case "":
		default:
			return errors.New("usage: sharetoken partner set -only on|off")
		}
		st.DailyLimitTokens, st.SharePct = *limit, *share
		if err := pc.SetShareSettings(cx, st); err != nil {
			return err
		}
		printShareSettings(st)
		return nil
	}
	return fmt.Errorf("unknown: partner %s (partner | partner invite | partner join <code|link> | partner rm <id> | partner revoke <id> | partner approve|reject|withdraw <id> | partner set)", sub)
}

func printShareSettings(st accounts.ShareSettings) {
	if st.OnlyPartners {
		fmt.Println("using: partners' nodes only, never the market")
	} else {
		fmt.Println("using: partners' nodes first, then the market (paid)")
	}
	limit := "no limit"
	if st.DailyLimitTokens > 0 {
		limit = fmt.Sprintf("%d tokens per 24 hours", st.DailyLimitTokens)
	}
	switch {
	case st.SharePct == 0:
		fmt.Printf("giving: paused (%s)\n", limit)
	default:
		fmt.Printf("giving: %d%% of each node's concurrent jobs, %s\n", st.SharePct, limit)
	}
}

func showPartners(cx context.Context, pc *client.PortalClient, base string) error {
	v, err := pc.Sharing(cx)
	if err != nil {
		return err
	}
	day := func(ns int64) string { return time.Unix(0, ns).Format("2006-01-02 15:04") }
	if len(v.Partners) == 0 {
		fmt.Println("no share partners; `sharetoken partner invite` makes an invite")
	} else {
		fmt.Printf("share partners (tokens in the last %d hours):\n", v.WindowHours)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tPARTNER\tSINCE\tYOU GAVE\tYOU USED")
		for _, p := range v.Partners {
			name := p.Username
			if p.Disabled {
				name += " (disabled)"
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%d\t%d\n", p.ID, name, day(p.Since), p.Given, p.Taken)
		}
		_ = tw.Flush()
	}
	fmt.Println()
	printShareSettings(v.Settings)
	if v.Settings.DailyLimitTokens > 0 {
		fmt.Printf("given in the last %d hours: %d of %d tokens\n", v.WindowHours, v.Served, v.Settings.DailyLimitTokens)
	}
	if len(v.Invites) > 0 {
		fmt.Println("\nopen invites:")
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tCODE\tGOOD UNTIL\tLINK")
		for _, inv := range v.Invites {
			link := ""
			if inv.LinkToken != "" {
				link = accounts.InviteLink(base, accounts.KindShare, inv.LinkToken)
			}
			if inv.Approval {
				link += " (approval)"
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", inv.ID, inv.Code, day(inv.ExpiresAt), link)
		}
		_ = tw.Flush()
	}
	printRequests(v.Requests, v.Waiting, "partner")
	return nil
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
		return errors.New("not logged in: run `sharetoken login` first")
	}
	sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := client.NewAgent(c.home, c.cfg)
	updates := client.NewUpdateWatcher(c.home, "cli", func() (string, string) { return c.cfg.Portal, c.cfg.PortalPin })
	a.Updates = updates
	go updates.Run(sctx) // a new release is written to the log
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
	if s, err := client.LoadAPIKey(c.home); err == nil && s.Required {
		fmt.Println("   API key required: `sharetoken apikey` shows it")
	} else if err == nil {
		fmt.Println("   no API key required (`sharetoken apikey on`)")
	}
	if st.Coordinator.Via != "" {
		fmt.Printf("   coordinator via relay %s\n", st.Coordinator.Via)
	}
	if st.Share.Running {
		for _, m := range st.Share.Serving {
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
	// The page reads the key from the address's fragment, which the browser
	// does not send anywhere (localauth.go).
	fmt.Printf("sharetoken daemon: app at http://%s/#key=%s (the key changes at each start)\n", d.Addr(), d.Key())
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
	addr, key := client.LocalAPI(c.home, c.cfg)
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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
			len(st.Share.Serving), s.JobsServed, s.JobsFailed, s.Active, credits(int64(s.EarnedMicros)))
		if s.AnnounceError != "" {
			fmt.Println("   offer not accepted:", s.AnnounceError)
		}
	} else {
		fmt.Println("not sharing")
	}
	if st.Tailnet != nil {
		printTailnet(*st.Tailnet)
	}
	for _, w := range st.Warnings {
		fmt.Println("warning:", w)
	}
	if st.Update != nil {
		printUpdate(*st.Update)
	}
	return nil
}

// checkVersion asks the portal of this state directory, or the default one,
// whether a newer release exists. It only reads the configuration.
func checkVersion(home string) error {
	base, pin := client.PortalAddr(), ""
	if _, err := os.Stat(filepath.Join(home, client.ConfigFile)); err == nil {
		if cfg, err := client.LoadConfig(home); err == nil && cfg.Portal != "" {
			base, pin = cfg.Portal, cfg.PortalPin
		}
	}
	cx, cancel := ctx()
	defer cancel()
	u, err := client.CheckUpdate(cx, base, pin, "cli")
	if err != nil {
		return fmt.Errorf("asking %s for the current release: %w", base, err)
	}
	if !u.Available {
		fmt.Println("up to date")
		return nil
	}
	printUpdate(u)
	return nil
}

// printUpdate tells the user about a newer release and where to get it.
func printUpdate(u client.Update) {
	if u.Required {
		fmt.Printf("upgrade required: this version is no longer served; jobs already made still settle, but new ones need %s\n", u.Latest)
	} else {
		fmt.Printf("update available: %s\n", u.Latest)
	}
	if n := u.Notes["en"]; n.Summary != "" {
		fmt.Printf("   %s\n", n.Summary)
	}
	if u.Package != nil {
		fmt.Printf("   download %s\n", u.Package.URL)
		if u.Package.SHA256 != "" {
			fmt.Printf("   sha256   %s\n", u.Package.SHA256)
		}
	} else if u.DownloadURL != "" {
		fmt.Printf("   download %s\n", u.DownloadURL)
	}
	if u.NotesURL != "" {
		fmt.Printf("   changes  %s\n", u.NotesURL)
	}
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
		if r.Private {
			// Never carries coordinator traffic, so COORD says nothing of it.
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t-\t%s\n", "private", first(relaySummary(r.Addr)), second(relaySummary(r.Addr)), rtt, map[bool]string{true: "sharing"}[r.Reserved])
			continue
		}
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

func first(a, _ string) string  { return a }
func second(_, b string) string { return b }

func (c *cmdCtx) relays(args []string) error {
	if len(args) > 0 && (args[0] == "add" || args[0] == "remove") {
		if len(args) != 2 {
			return fmt.Errorf("usage: sharetoken relays %s <address>", args[0])
		}
		method := map[string]string{"add": http.MethodPost, "remove": http.MethodDelete}[args[0]]
		var rs []client.RelayView
		if err := c.daemon(method, "/api/v1/relays/private", map[string]string{"addr": args[1]}, &rs); err != nil {
			return err
		}
		printRelays(rs)
		return nil
	}
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

// tailnet shows or changes the direct path over tunnels (client/tailnet.go).
// The settings are saved first and a running client takes them up at once.
func (c *cmdCtx) tailnet(args []string) error {
	if len(args) > 0 {
		tc := c.cfg.Tailnet
		switch args[0] {
		case "on":
			fs := flag.NewFlagSet("tailnet on", flag.ExitOnError)
			derp := fs.String("derp-map", tc.DERPMap, "URL or file of the DERP map (empty: tailcat's public relays)")
			wait := fs.Int("wait", tc.WaitMillis, "milliseconds a request waits for a new tunnel before it uses the relay")
			proto := fs.String("transport", tc.Transport, "what a connection through a tunnel runs on: quic (the default) or tcp")
			_ = fs.Parse(args[1:])
			if fs.NArg() > 0 {
				return errors.New("usage: sharetoken tailnet on [-derp-map URL|FILE] [-wait MS] [-transport quic|tcp]")
			}
			if *proto != "" && *proto != "quic" && *proto != "tcp" {
				return errors.New("-transport must be quic or tcp")
			}
			if *proto == "quic" {
				*proto = "" // the default is not written down
			}
			tc.Transport = *proto
			if d := *derp; d != "" && !strings.HasPrefix(d, "https://") && !strings.HasPrefix(d, "http://") {
				abs, err := filepath.Abs(strings.TrimPrefix(d, "file://"))
				if err != nil {
					return err
				}
				if _, err := os.Stat(abs); err != nil {
					return fmt.Errorf("DERP map: %w", err)
				}
				*derp = abs
			}
			tc.Enabled, tc.DERPMap, tc.WaitMillis = true, *derp, *wait
		case "off":
			if len(args) != 1 {
				return errors.New("usage: sharetoken tailnet off")
			}
			tc.Enabled = false
		default:
			return fmt.Errorf("unknown tailnet command %q (on, off)", args[0])
		}
		if tc.WaitMillis < 0 || tc.WaitMillis > client.MaxTailnetWaitMillis {
			return fmt.Errorf("-wait must be between 0 and %d", client.MaxTailnetWaitMillis)
		}
		c.cfg.Tailnet = tc
		if err := c.save(); err != nil {
			return err
		}
		if err := c.daemon(http.MethodPost, "/api/v1/tailnet/reload", nil, nil); err != nil && !strings.Contains(err.Error(), "not running") {
			return fmt.Errorf("saved, but the running client refused it: %w", err)
		}
	}
	var st client.TailnetStatus
	if err := c.daemon(http.MethodGet, "/api/v1/tailnet", nil, &st); err != nil {
		if !strings.Contains(err.Error(), "not running") {
			return err
		}
		state := "off"
		if c.cfg.Tailnet.Enabled {
			state = "on"
		}
		fmt.Printf("tailnet %s (the client is not running)\n", state)
		return nil
	}
	printTailnet(st)
	return nil
}

func printTailnet(st client.TailnetStatus) {
	if !st.Enabled {
		fmt.Println("tailnet off (sharetoken tailnet on)")
		return
	}
	fmt.Printf("tailnet on (%s), relays from %s", st.Proto, st.DERPMap)
	if st.WaitMillis > 0 {
		fmt.Printf(", a request waits up to %d ms for a new tunnel", st.WaitMillis)
	}
	fmt.Println()
	switch {
	case st.Serving:
		fmt.Printf("   reachable as a seller through DERP region %s\n", st.Region)
	case st.Error != "":
		fmt.Println("   not reachable as a seller:", st.Error)
	default:
		fmt.Println("   not serving (sharing is off, or the tunnel is starting)")
	}
	if len(st.System) > 0 {
		fmt.Printf("   this machine is on a Tailscale network (%s): a seller announces that address too\n", strings.Join(st.System, ", "))
	}
	if len(st.Tunnels) == 0 {
		fmt.Println("   no tunnels")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "   \tPEER\tPATH\tOVER\tENDPOINT\tRTT\tCONNS\tRX\tTX")
	for _, t := range st.Tunnels {
		dir := map[string]string{"out": "to seller", "in": "from buyer"}[t.Dir]
		rtt := ""
		if t.Millis > 0 {
			rtt = fmt.Sprintf("%.1f ms", t.Millis)
		}
		conns, rx, tx := "", "", ""
		if t.Dir == "out" {
			conns = strconv.Itoa(t.Conns)
		} else {
			rx, tx = strconv.FormatInt(t.RxBytes, 10), strconv.FormatInt(t.TxBytes, 10)
		}
		fmt.Fprintf(tw, "   %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", dir, short(t.Peer), t.Path, t.Proto, t.Endpoint, rtt, conns, rx, tx)
	}
	_ = tw.Flush()
}

func (c *cmdCtx) models(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	codex := fs.String("codex", "", "write the models as a Codex model catalog to this file (model_catalog_json in ~/.codex/config.toml)")
	_ = fs.Parse(args)
	var out struct {
		Models []protocol.ModelSummary `json:"models"`
	}
	if err := c.daemon(http.MethodGet, "/api/v1/models", nil, &out); err != nil {
		return err
	}
	if *codex != "" {
		return writeCodexCatalog(*codex, out.Models)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tNODES\tTEE\tMIN IN/Mtok\tMIN OUT/Mtok")
	for _, m := range out.Models {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", m.Model, m.Nodes, m.AttestedNodes, m.MinPriceIn, m.MinPriceOut)
	}
	return tw.Flush()
}

// writeCodexCatalog writes ms to path as a Codex model catalog and says how
// to use it. Codex reads the file when it starts, so it is written again
// when the models change.
func writeCodexCatalog(path string, ms []protocol.ModelSummary) error {
	data, err := json.MarshalIndent(buyer.CodexCatalog(ms), "", "  ")
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		return err
	}
	fmt.Printf("wrote %d models to %s\nin ~/.codex/config.toml, at the top level:\n  model_catalog_json = %q\n", len(ms), abs, abs)
	return nil
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
		from := fs.String("provider", "", "the provider whose model this is (default: the local model server)")
		maxOut := fs.Uint64("max-output", 0, "the model's own limit of output tokens per request, if it refuses the usual one")
		cacheRead := fs.Int64("cache-read", -1, "price per million prompt tokens read from the prompt cache (default: a tenth of -in)")
		cacheWrite := fs.Int64("cache-write", -1, "price per million prompt tokens written to the prompt cache (default: 1.25 x -in for Anthropic models, else -in)")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return errors.New("usage: sharetoken share add <model> [-provider NAME] [-id ID] [-in P] [-out P] [-cache-read P] [-cache-write P] [-max-output N]")
		}
		upstreamModel := args[1]
		_ = fs.Parse(args[2:])
		if *id == "" {
			*id = upstreamModel
		}
		m := node.ModelConfig{ID: *id, UpstreamModel: upstreamModel, PriceInMicros: *in, PriceOutMicros: *out, MaxOutputTokens: *maxOut}
		if *cacheRead >= 0 {
			v := uint64(*cacheRead)
			m.PriceCacheRead = &v
		}
		if *cacheWrite >= 0 {
			v := uint64(*cacheWrite)
			m.PriceCacheWrite = &v
		}
		if *from != "" && *from != "local" {
			var local []string
			for _, lm := range sc.Models {
				local = append(local, lm.ID)
			}
			if _, err := provider.SetModel(c.home, *from, m, local); err != nil {
				return err
			}
		} else {
			if f, err := provider.Load(c.home); err != nil {
				return err
			} else {
				for _, e := range f.Providers {
					for _, pm := range e.Models {
						if pm.ID == *id {
							return fmt.Errorf("provider %s already sells %s: choose another ID with -id", e.Name, *id)
						}
					}
				}
			}
			sc.Models = append(slices.DeleteFunc(sc.Models, func(o node.ModelConfig) bool { return o.ID == *id }), m)
		}
		sc.Enabled = true
	case "rm":
		if len(args) != 2 {
			return errors.New("usage: sharetoken share rm <id>")
		}
		n := len(sc.Models)
		sc.Models = slices.DeleteFunc(sc.Models, func(o node.ModelConfig) bool { return o.ID == args[1] })
		if len(sc.Models) == n {
			from, err := provider.RemoveModel(c.home, args[1])
			if err != nil {
				return err
			}
			if from == "" {
				return fmt.Errorf("not sharing %q", args[1])
			}
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
	if err := c.reload(); err != nil {
		return err
	}
	return c.showShare()
}

// reload has a running client take up changed sharing settings.
func (c *cmdCtx) reload() error {
	if err := c.daemon(http.MethodPost, "/api/v1/share/reload", nil, nil); err != nil && !strings.Contains(err.Error(), "not running") {
		return fmt.Errorf("saved, but the running client refused it: %w", err)
	}
	return nil
}

func (c *cmdCtx) showShare() error {
	sc := c.cfg.Share
	state := "off"
	if sc.Enabled {
		state = "on"
	}
	fmt.Printf("sharing %s, concurrency %d, relays %d\n", state, sc.Concurrency, sc.Relays)
	printModels := func(ms []node.ModelConfig) {
		for _, m := range ms {
			limit := ""
			if m.MaxOutputTokens > 0 {
				limit = fmt.Sprintf(" max-output=%d", m.MaxOutputTokens)
			}
			fmt.Printf("      %s (upstream %s) in=%d out=%d micro-credits/Mtok%s\n", m.ID, m.UpstreamModel, m.PriceInMicros, m.PriceOutMicros, limit)
		}
	}
	total := len(sc.Models)
	if len(sc.Models) > 0 {
		fmt.Printf("   local model server %s\n", sc.Upstream)
		printModels(sc.Models)
	}
	views, err := provider.Views(c.home)
	if err != nil {
		return err
	}
	for _, v := range views {
		state := "on"
		if !v.Enabled {
			state = "off"
		}
		fmt.Printf("   provider %s (%s, %s) %s\n", v.Name, v.TypeName, v.Auth, state)
		printModels(v.Models)
		if len(v.Models) == 0 {
			fmt.Printf("      no models: `sharetoken provider models %s`, then `sharetoken share add <model> -provider %s`\n", v.Name, v.Name)
		}
		total += len(v.Models)
	}
	if total == 0 && len(views) == 0 {
		fmt.Println("   no models: `sharetoken share detect`, then `sharetoken share add <model>`; for a hosted API see `sharetoken provider`")
	}
	return nil
}

// ---- providers ----

func (c *cmdCtx) provider(args []string) error {
	if len(args) == 0 {
		return c.showProviders()
	}
	usageErr := func(u string) error { return errors.New("usage: sharetoken provider " + u) }
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("provider add", flag.ExitOnError)
		options := addProviderOptionFlags(fs)
		name := fs.String("name", "", "your name for it (default: the kind)")
		base := fs.String("url", "", "base URL of the API (needed for custom; otherwise the kind's own)")
		noCheck := fs.Bool("no-check", false, "save it without asking the provider whether the key works")
		var headers cli.Repeated
		fs.Var(&headers, "header", "an extra request header as Name=value (repeatable)")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return usageErr("add <kind> [-name N] [-url URL] [-header K=V] [-no-check]")
		}
		_ = fs.Parse(args[2:])
		transport, anthropic := options.values(fs)
		kind, ok := provider.LookupKind(args[1])
		if !ok {
			return fmt.Errorf("unknown kind %q: `sharetoken provider` lists them", args[1])
		}
		if !kind.APIKey {
			return fmt.Errorf("%s has no API-key access: `sharetoken provider login %s`", kind.Name, kind.ID)
		}
		req := provider.AddRequest{Type: kind.ID, Name: *name, BaseURL: *base, NoCheck: *noCheck, Transport: transport, Anthropic: anthropic}
		for _, h := range headers {
			k, v, ok := strings.Cut(h, "=")
			if !ok {
				return fmt.Errorf("-header wants Name=value, got %q", h)
			}
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			req.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if kind.BaseURL == "" && req.BaseURL == "" {
			return fmt.Errorf("%s needs the base URL of the API: -url https://…/v1", kind.Name)
		}
		// Not a flag: arguments show up in the process list and the shell history.
		req.APIKey = strings.TrimSpace(os.Getenv("SHARETOKEN_PROVIDER_KEY"))
		if req.APIKey == "" {
			prompt := kind.Name + " API key: "
			if kind.KeyOptional {
				prompt = kind.Name + " API key (empty if the API needs none): "
			}
			var err error
			if req.APIKey, err = cli.ReadSecret(prompt); err != nil && !kind.KeyOptional {
				return err
			}
		}
		cx, cancel := ctx()
		defer cancel()
		added, err := provider.AddWithKey(cx, c.home, req)
		if err != nil {
			if errors.Is(err, provider.ErrKeyRefused) {
				return fmt.Errorf("%w; nothing was saved", err)
			}
			if strings.Contains(err.Error(), "anthropic-workspace-id") {
				return fmt.Errorf("%w\n   this key is not tied to a workspace: add -header anthropic-workspace-id=<workspace ID>", err)
			}
			return err
		}
		printAdded(added)
	case "login":
		fs := flag.NewFlagSet("provider login", flag.ExitOnError)
		options := addProviderOptionFlags(fs)
		sessionKey := fs.Bool("session-key", false, "Anthropic: sign in with a sessionKey cookie using a hidden prompt")
		mode := fs.String("mode", "", "Anthropic authorization scope: full or inference")
		name := fs.String("name", "", "your name for it (default: the kind)")
		project := fs.String("project", "", "Google Cloud project ID, for a Google account that has no Code Assist project of its own")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return usageErr("login <kind> [-name N] [-project ID]")
		}
		_ = fs.Parse(args[2:])
		transport, anthropic := options.values(fs)
		if *sessionKey {
			if !strings.EqualFold(args[1], provider.KindAnthropic) {
				return errors.New("-session-key requires Anthropic")
			}
			added, err := c.providerSessionLogin(*name, provider.LoginOptions{Mode: *mode, Transport: transport, Anthropic: anthropic})
			if err != nil {
				return err
			}
			printAdded(added)
			break
		}
		login, err := provider.StartProviderLogin(c.home, *name, args[1], provider.LoginOptions{Project: *project, Mode: *mode, Transport: transport, Anthropic: anthropic})
		if err != nil {
			return err
		}
		fmt.Printf("Open this address in a browser and sign in:\n\n%s\n\n%s\n", login.URL, login.Hint)
		input, err := cli.Prompt("Paste it here: ")
		if err != nil {
			return err
		}
		cx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		added, err := login.FinishAndSave(cx, c.home, input, *name)
		if err != nil {
			return err
		}
		printAdded(added)
	case "configure":
		fs := flag.NewFlagSet("provider configure", flag.ExitOnError)
		options := addProviderOptionFlags(fs)
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return usageErr("configure <name> [-proxy-mode environment|direct|custom] [-profile standard|claude-code] [-sampling strict|default]")
		}
		_ = fs.Parse(args[2:])
		transport, anthropic := options.values(fs)
		if anthropic != nil {
			f, err := provider.Load(c.home)
			if err != nil {
				return err
			}
			e, err := f.Find(args[1])
			if err != nil {
				return err
			}
			if e.Anthropic != nil {
				if anthropic.Profile == "" {
					anthropic.Profile = e.Anthropic.Profile
				}
				if anthropic.Sampling == "" {
					anthropic.Sampling = e.Anthropic.Sampling
				}
			}
		}
		if _, err := provider.Configure(c.home, args[1], provider.ConfigureRequest{Transport: transport, Anthropic: anthropic}); err != nil {
			return err
		}
	case "rm":
		if len(args) != 2 {
			return usageErr("rm <name>")
		}
		if err := provider.Remove(c.home, args[1]); err != nil {
			return err
		}
		fmt.Printf("removed %s with its credentials\n", args[1])
	case "on", "off":
		if len(args) != 2 {
			return usageErr(args[0] + " <name>")
		}
		if _, err := provider.SetEnabled(c.home, args[1], args[0] == "on"); err != nil {
			return err
		}
	case "models":
		if len(args) != 2 {
			return usageErr("models <name>")
		}
		cx, cancel := ctx()
		defer cancel()
		discovery, err := provider.Discover(cx, c.home, args[1])
		if err != nil {
			return err
		}
		if discovery.Warning != "" {
			fmt.Fprintln(os.Stderr, discovery.Warning)
		}
		for _, m := range discovery.Models {
			fmt.Println(m)
		}
		return nil
	case "warmup":
		return c.providerWarmup(args[1:])
	case "test":
		fs := flag.NewFlagSet("provider test", flag.ExitOnError)
		model := fs.String("model", "", "the provider's model to ask (default: the first one it sells)")
		prompt := fs.String("prompt", "", "what to ask")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return usageErr("test <name> [-model M] [-prompt TEXT]")
		}
		_ = fs.Parse(args[2:])
		cx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		res, used, err := provider.Test(cx, c.home, args[1], *model, *prompt)
		if err != nil {
			return err
		}
		fmt.Printf("%s answered: %q\n", used, strings.TrimSpace(res.Text))
		fmt.Printf("   finish %s, %d prompt + %d completion tokens, %d chunks\n", res.FinishReason, res.PromptTokens, res.CompletionTokens, res.Chunks)
		if res.PromptReported > 0 {
			fmt.Printf("   upstream input: %d tokens; billable input estimate: %d (the seller pays for adapter instructions)\n", res.PromptReported, res.PromptTokens)
		}
		if res.Reported > 0 {
			fmt.Printf("   note: the provider counted %d completion tokens, more than the answer shows (hidden reasoning); buyers pay for at most %d of them\n", res.Reported, res.CompletionTokens)
		}
		if res.PromptTokens == 0 && res.CompletionTokens == 0 {
			fmt.Println("   warning: no token counts came back: jobs would be billed by estimate")
		}
		return nil
	default:
		return fmt.Errorf("unknown provider command %q", args[0])
	}
	if err := c.reload(); err != nil {
		return err
	}
	return c.showProviders()
}

func printAdded(a *provider.Added) {
	v := a.Provider
	who := v.Key
	if v.Account != "" {
		who = v.Account
	}
	fmt.Printf("saved provider %s (%s, %s) %s\n", v.Name, v.TypeName, v.Auth, who)
	if a.Check != nil {
		if a.Check.Warning != "" {
			fmt.Println("   note:", a.Check.Warning)
		}
		fmt.Printf("   models: %s\n", strings.Join(a.Check.Models, ", "))
		if a.Check.Balance != "" {
			fmt.Println("   balance", a.Check.Balance)
		}
		if a.Check.Unusable {
			fmt.Println("   warning: the account cannot pay for requests now; jobs will fail until it is topped up")
		}
	}
	if len(v.Models) == 0 {
		fmt.Printf("next: `sharetoken share add <model> -provider %s`\n", v.Name)
	}
}

type providerOptionFlags struct{ proxyMode, proxyURL, profile, sampling, tlsProfile, tokenTLSProfile *string }

func addProviderOptionFlags(fs *flag.FlagSet) providerOptionFlags {
	return providerOptionFlags{
		proxyMode:       fs.String("proxy-mode", "", "environment, direct or custom"),
		proxyURL:        fs.String("proxy", "", "proxy URL; use SHARETOKEN_PROVIDER_PROXY for a URL containing a password"),
		profile:         fs.String("profile", "", "Anthropic: standard or claude-code OAuth request profile"),
		sampling:        fs.String("sampling", "", "Anthropic: default (leave out sampling parameters a model refuses), or strict to refuse the request"),
		tlsProfile:      fs.String("tls-profile", "", "inference TLS transport: native or chrome"),
		tokenTLSProfile: fs.String("token-tls-profile", "", "login and refresh TLS transport: native or chrome"),
	}
}

func (o providerOptionFlags) values(fs *flag.FlagSet) (*provider.TransportOptions, *provider.AnthropicOptions) {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	var tr *provider.TransportOptions
	if seen["proxy-mode"] || seen["proxy"] || seen["tls-profile"] || seen["token-tls-profile"] {
		tr = &provider.TransportOptions{ProxyMode: *o.proxyMode, ProxyURL: *o.proxyURL, TLSProfile: *o.tlsProfile, TokenTLSProfile: *o.tokenTLSProfile}
		if tr.ProxyURL == "" && tr.ProxyMode == "custom" {
			tr.ProxyURL = os.Getenv("SHARETOKEN_PROVIDER_PROXY")
		}
		if tr.ProxyURL != "" && tr.ProxyMode == "" {
			tr.ProxyMode = "custom"
		}
	}
	var anth *provider.AnthropicOptions
	if seen["profile"] || seen["sampling"] {
		anth = &provider.AnthropicOptions{Profile: *o.profile, Sampling: *o.sampling}
	}
	return tr, anth
}

// showProviders lists the kinds that can be added and the providers configured.
func (c *cmdCtx) showProviders() error {
	views, err := provider.Views(c.home)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if len(views) > 0 {
		fmt.Fprintln(tw, "NAME\tKIND\tAUTH\tSTATE\tACCOUNT\tMODELS")
		for _, v := range views {
			state := "on"
			if !v.Enabled {
				state = "off"
			}
			who := v.Key
			if v.Auth == provider.AuthOAuth {
				who = cmpOr(v.Account, "logged in")
				if v.Plan != "" {
					who += " (" + v.Plan + ")"
				}
			}
			var ids []string
			for _, m := range v.Models {
				ids = append(ids, m.ID)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.Type, v.Auth, state, cmpOr(who, "-"), cmpOr(strings.Join(ids, ","), "-"))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Println()
	}
	fmt.Fprintln(tw, "KIND\tADD WITH\tNOTE")
	for _, k := range provider.KindViews() {
		var how []string
		if k.APIKey {
			how = append(how, "provider add "+k.ID)
		}
		if k.OAuth {
			how = append(how, "provider login "+k.ID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", k.ID, strings.Join(how, " | "), k.Note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("credentials are kept in %s\n", provider.Path(c.home))
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

// ---- skill for coding agents ----

func (c *cmdCtx) skill(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("skill "+sub, flag.ExitOnError)
	which := fs.String("agent", "", "claude, codex or all (default: every agent found on this machine)")
	force := fs.Bool("force", false, "replace or remove a SKILL.md that sharetoken did not write")
	_ = fs.Parse(args)
	agents, err := agentskill.DefaultAgents()
	if err != nil {
		return err
	}
	content := agentskill.Render(c.skillParams())
	switch sub {
	case "", "status", "ls", "list":
		for _, a := range agents {
			state := "not found"
			if a.Present() {
				state = "found"
			}
			switch present, current := agentskill.Installed(a, content); {
			case current:
				state += ", skill installed"
			case present:
				state += ", skill installed but out of date: `sharetoken skill install`"
			case a.Present():
				state += ", no skill: `sharetoken skill install`"
			}
			fmt.Printf("%-12s %s\n             %s\n", a.Name, state, a.Path())
		}
	case "install", "add":
		sel, err := agentskill.Select(agents, *which)
		if err != nil {
			return err
		}
		var failed error
		for _, a := range sel {
			res, err := agentskill.Install(a, content, *force)
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "%s: %v\n", a.Name, err)
				failed = errors.New("not every agent got the skill")
			case res.Changed:
				fmt.Printf("%s: installed %s\n", a.Name, res.Path)
			default:
				fmt.Printf("%s: already up to date, %s\n", a.Name, res.Path)
			}
		}
		if failed == nil {
			fmt.Println("an agent session that is already open picks it up when it is started again")
		}
		return failed
	case "uninstall", "rm", "remove":
		sel, err := agentskill.Select(agents, cmpOr(*which, "all"))
		if err != nil {
			return err
		}
		var failed error
		for _, a := range sel {
			res, err := agentskill.Remove(a, *force)
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "%s: %v\n", a.Name, err)
				failed = errors.New("not every copy was removed")
			case res.Changed:
				fmt.Printf("%s: removed %s\n", a.Name, res.Path)
			default:
				fmt.Printf("%s: not installed\n", a.Name)
			}
		}
		return failed
	case "show", "cat":
		fmt.Print(content)
	default:
		return errors.New("usage: sharetoken skill [install|uninstall|show] [-agent claude|codex|all] [-force]")
	}
	return nil
}

// skillParams describe this client to the skill: how to run it and where it
// listens.
func (c *cmdCtx) skillParams() agentskill.Params {
	portal := cmpOr(c.cfg.Portal, client.PortalAddr())
	return agentskill.Params{
		Command: commandName(),
		Env:     client.Env,
		Portal:  portal,
		Home:    c.home,
		HTTP:    cmpOr(c.cfg.HTTP, client.DefaultHTTP()),
		OS:      runtime.GOOS,
	}
}

// commandName is "sharetoken" when that name on the PATH is this program, else
// the full path of this program.
func commandName() string {
	self, err := os.Executable()
	if err != nil {
		return "sharetoken"
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	if p, err := exec.LookPath("sharetoken"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil && sameFile(real, self) {
			return "sharetoken"
		}
	}
	return self
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}
