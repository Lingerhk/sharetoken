// Command coordinator runs the sharetoken accounting platform: matching,
// tickets, settlement, disputes and audits for an open-weight model network.
//
// It needs no public address. It holds a reservation on every relay (the
// enabled relays registered in the database through the portal, plus any
// -relay), and users reach it only through those relays. It shares its
// database with the portal, which manages users, devices and credits.
//
//	coordinator -config coordinator.yaml                 # serve (configs/coordinator.example.yaml)
//	coordinator -db mysql://sharetoken@127.0.0.1/sharetoken  # or flags only
//	coordinator -reference id=qwen2.5-7b,upstream=qwen2.5:7b,url=http://127.0.0.1:11434/v1
//	                                                     # + audit nodes serving qwen2.5-7b
//	coordinator balance <peer-id>                        # operator commands, run beside
//	coordinator disputes | nodes | audits | relays | check   # the live service on its database
//	coordinator resolve <job-id> pay|refund
//	coordinator audit calibrate [model]                  # measure honest scores, set audit thresholds
//	coordinator audit try <node-peer-id> <model>         # audit one node now (through official relays)
//	coordinator audit calibrations                       # list calibrations
//	coordinator id                                       # peer ID, for the allow_peers of relays
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // served only with debug.pprof_listen / -debug
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"go.yaml.in/yaml/v3"

	"sharetoken/internal/accounts"
	"sharetoken/internal/attest"
	"sharetoken/internal/audit"
	"sharetoken/internal/cli"
	"sharetoken/internal/config"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/tokens"
	"sharetoken/internal/version"
)

// Reference is a reference copy of a model (references[] in coordinator.yaml).
type Reference struct {
	ID         string `yaml:"id"`           // marketplace model id
	Upstream   string `yaml:"upstream"`     // model name on the server
	URL        string `yaml:"url"`          // OpenAI-compatible base URL
	APIKeyEnv  string `yaml:"api_key_env"`  // environment variable holding its API key
	APIKeyFile string `yaml:"api_key_file"` // or a file holding it (wins over api_key_env)
	// Verify is how audits check answers against it: vllm, deepseek-prefix,
	// openai-div or none (bill only; default).
	Verify string `yaml:"verify"`
	// VerifyURL, if set, is the base URL audits score against (default url).
	VerifyURL string `yaml:"verify_url"`
	// Params are extra fields of every audit request, e.g.
	// {reasoning_effort: none} for a model that thinks by default.
	Params map[string]any `yaml:"params"`
}

// File is coordinator.yaml (configs/coordinator.example.yaml).
type File struct {
	Version  int `yaml:"version"`
	Identity struct {
		KeyFile string `yaml:"key_file"`
	} `yaml:"identity"`
	Database struct {
		config.Database  `yaml:",inline"`
		StartupBusyRetry config.Duration `yaml:"startup_busy_retry"`
	} `yaml:"database"`
	Network struct {
		Listen    []string        `yaml:"listen"`
		MaxConns  int             `yaml:"max_conns"`
		Relays    []string        `yaml:"relays"`
		RelaySync config.Duration `yaml:"relay_sync"`
	} `yaml:"network"`
	Market struct {
		FeeBps          int64           `yaml:"fee_bps"`
		MinBudgetMicros uint64          `yaml:"min_budget_micros"`
		WarmUp          config.Duration `yaml:"warm_up"`
	} `yaml:"market"`
	Jobs struct {
		TicketTTL       config.Duration `yaml:"ticket_ttl"`
		StartGrace      config.Duration `yaml:"start_grace"`
		ReconnectGrace  config.Duration `yaml:"reconnect_grace"`
		MaxStaleTickets int             `yaml:"max_stale_tickets"`
		StaleAfter      config.Duration `yaml:"stale_after"`
		SettleGrace     config.Duration `yaml:"settle_grace"`
		DisputeWindow   config.Duration `yaml:"dispute_window"`
		MaxOfferTTL     config.Duration `yaml:"max_offer_ttl"`
		JobRetention    config.Duration `yaml:"job_retention"`
		SuspendFor      config.Duration `yaml:"suspend_for"`
	} `yaml:"jobs"`
	References []Reference `yaml:"references"`
	Audit      struct {
		Interval         config.Duration `yaml:"interval"`
		BudgetMicros     uint64          `yaml:"budget_micros"`
		PromptsFile      string          `yaml:"prompts_file"`
		MaxTokens        int             `yaml:"max_tokens"`
		Questions        int             `yaml:"questions"`
		Spread           config.Duration `yaml:"spread"`
		ScoreTokens      int             `yaml:"score_tokens"`
		Threshold        float64         `yaml:"threshold"`
		Margin           float64         `yaml:"margin"`
		DivFails         int             `yaml:"div_fails"`
		Shadow           bool            `yaml:"shadow"`
		CalibrateEvery   config.Duration `yaml:"calibrate_every"`
		CalibrateSamples int             `yaml:"calibrate_samples"`
	} `yaml:"audit"`
	TEE struct {
		PolicyFile  string `yaml:"policy_file"`
		OnlineCheck bool   `yaml:"online_check"`
	} `yaml:"tee"`
	Debug struct {
		PprofListen string `yaml:"pprof_listen"`
	} `yaml:"debug"`
	Log config.Log `yaml:"log"`
	Dev struct {
		OpenAccounts bool   `yaml:"open_accounts"`
		CreditMicros uint64 `yaml:"credit_micros"`
	} `yaml:"dev"`
}

func defaults() File {
	var f File
	f.Version = config.Version
	f.Identity.KeyFile = "coordinator.key"
	f.Database.Database = config.DefaultDatabase("")
	f.Database.StartupBusyRetry = config.D("2m")
	f.Network.MaxConns = 8192
	f.Network.RelaySync = config.D("15s")
	f.Market.FeeBps, f.Market.MinBudgetMicros, f.Market.WarmUp = 500, 1, config.D("30s")
	d := coordinator.Defaults()
	j := &f.Jobs
	j.TicketTTL, j.StartGrace, j.ReconnectGrace = config.Duration(d.TicketTTL), config.Duration(d.StartGrace), config.Duration(d.ReconnectGrace)
	j.MaxStaleTickets, j.StaleAfter = d.MaxStaleTickets, config.Duration(d.StaleAfter)
	j.SettleGrace, j.DisputeWindow, j.MaxOfferTTL = config.Duration(d.SettleGrace), config.Duration(d.DisputeWindow), config.Duration(d.MaxOfferTTL)
	j.JobRetention, j.SuspendFor = config.Duration(d.JobRetention), config.Duration(d.SuspendFor)
	f.Market.MinBudgetMicros = d.MinBudgetMicros
	a := &f.Audit
	a.Interval, a.BudgetMicros, a.MaxTokens = config.D("10m"), 200_000, 96
	a.Questions, a.Spread, a.ScoreTokens, a.Threshold, a.Margin, a.DivFails = 5, config.D("2m"), 64, 0.85, 0.03, 2
	a.Shadow, a.CalibrateEvery, a.CalibrateSamples = true, config.D("24h"), 40
	f.Log = config.DefaultLog()
	return f
}

// load builds the settings: defaults, then the -config file, then explicit
// flags. It returns the remaining arguments (the command).
func load(args []string) (File, []string, error) {
	f := defaults()
	if path := config.ConfigPath(args); path != "" {
		if err := config.Load(path, &f); err != nil {
			return f, nil, err
		}
		config.Resolve(config.Dir(path), &f.Identity.KeyFile, &f.Database.DSN, &f.Database.PasswordFile,
			&f.Audit.PromptsFile, &f.TEE.PolicyFile, &f.Log.File)
		for i := range f.References {
			config.Resolve(config.Dir(path), &f.References[i].APIKeyFile)
		}
	}
	fs := flag.NewFlagSet("coordinator", flag.ExitOnError)
	fl := config.Flags{FlagSet: fs}
	fs.String("config", "", "YAML configuration file (configs/coordinator.example.yaml); flags given explicitly override it")
	fs.StringVar(&f.Identity.KeyFile, "key", f.Identity.KeyFile, "identity seed file (created with mode 0600 if missing)")
	fs.StringVar(&f.Database.DSN, "db", f.Database.DSN, `database shared with the portal: mysql://user@host:port/db (password from database.password_env/password_file, default $SHARETOKEN_DB_PASSWORD)`)
	fl.List(&f.Network.Listen, "listen", "comma-separated listen multiaddrs (default: none; the coordinator is reachable only through its relays)")
	fs.Int64Var(&f.Market.FeeBps, "fee-bps", f.Market.FeeBps, "platform fee, in basis points of each settled job")
	fl.Repeated(&f.Network.Relays, "relay", "relay to reserve on, as a full multiaddr ending in /p2p/<id> (repeatable), besides those registered in the database")
	fs.IntVar(&f.Network.MaxConns, "max-conns", f.Network.MaxConns, "connection-manager high watermark (every online user holds a relayed connection)")
	fs.BoolVar(&f.Dev.OpenAccounts, "open-accounts", f.Dev.OpenAccounts, "DEV ONLY: let peers that are not bound to a user trade from their own account")
	fs.Uint64Var(&f.Dev.CreditMicros, "dev-credit", f.Dev.CreditMicros, "DEV ONLY: credit each new peer this many micro-credits on first use")
	var refs []string
	fl.Repeated(&refs, "reference", "reference copy of a model (repeatable, replaces references[]): id=<marketplace id>,upstream=<server model name>,url=<OpenAI-compatible base URL>[,key-env=<VAR>][,verify=vllm|deepseek-prefix|openai-div|none]")
	fl.Duration(&f.Audit.Interval, "audit-interval", "mean time between audits of nodes serving a referenced model (0 disables)")
	fs.Uint64Var(&f.Audit.BudgetMicros, "audit-budget", f.Audit.BudgetMicros, "micro-credits reserved per audit job, paid from platform fees")
	fs.StringVar(&f.Audit.PromptsFile, "audit-prompts", f.Audit.PromptsFile, "private question pool: one question per line, plain text or {\"system\":...,\"messages\":[...]} (default: built-in open questions)")
	fs.BoolVar(&f.Audit.Shadow, "audit-shadow", f.Audit.Shadow, "record audit verdicts without touching nodes' records")
	fs.IntVar(&f.Audit.Questions, "audit-questions", f.Audit.Questions, "questions per audit")
	fl.Duration(&f.Audit.Spread, "audit-spread", "random pause of up to this long between an audit's questions")
	fs.StringVar(&f.TEE.PolicyFile, "tee-policy", f.TEE.PolicyFile, "JSON allow-list of TEE images; enables require_tee matching (see attest.LoadPolicy)")
	fs.BoolVar(&f.TEE.OnlineCheck, "tee-online", f.TEE.OnlineCheck, "also check TEE revocations / TCB status with AMD and Intel (network)")
	fs.StringVar(&f.Debug.PprofListen, "debug", f.Debug.PprofListen, "serve Go pprof profiles at http://<addr>/debug/pprof/ (keep it on loopback), e.g. 127.0.0.1:6060")
	if err := fs.Parse(args); err != nil {
		return f, nil, err
	}
	if refs != nil {
		parsed, err := parseReferenceFlags(refs)
		if err != nil {
			return f, nil, fmt.Errorf("-reference: %w", err)
		}
		f.References = parsed
	}
	return f, fs.Args(), nil
}

// validate reports every inconsistency at once.
func (f *File) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if f.Database.DSN == "" {
		bad("database.dsn is empty")
	}
	if f.Market.FeeBps < 0 || f.Market.FeeBps > 10_000 {
		bad("market.fee_bps %d: want 0..10000", f.Market.FeeBps)
	}
	j := f.Jobs
	for name, d := range map[string]config.Duration{"ticket_ttl": j.TicketTTL, "start_grace": j.StartGrace, "reconnect_grace": j.ReconnectGrace,
		"stale_after": j.StaleAfter, "settle_grace": j.SettleGrace, "dispute_window": j.DisputeWindow, "max_offer_ttl": j.MaxOfferTTL,
		"job_retention": j.JobRetention, "suspend_for": j.SuspendFor} {
		if d <= 0 {
			bad("jobs.%s must be positive", name)
		}
	}
	if j.MaxStaleTickets <= 0 {
		bad("jobs.max_stale_tickets must be positive")
	}
	if j.TicketTTL > 0 && j.TicketTTL < config.D("10s") {
		bad("jobs.ticket_ttl %v: nodes need at least 10s to start a job", j.TicketTTL)
	}
	if j.StartGrace >= j.SettleGrace {
		bad("jobs.start_grace (%v) must be shorter than jobs.settle_grace (%v)", j.StartGrace, j.SettleGrace)
	}
	if j.SettleGrace >= j.DisputeWindow {
		bad("jobs.settle_grace (%v) must be shorter than jobs.dispute_window (%v)", j.SettleGrace, j.DisputeWindow)
	}
	if j.JobRetention < j.DisputeWindow {
		bad("jobs.job_retention (%v) must be at least jobs.dispute_window (%v): disputes need the job", j.JobRetention, j.DisputeWindow)
	}
	if f.Network.RelaySync <= 0 {
		bad("network.relay_sync must be positive")
	}
	a := f.Audit
	if a.Interval < 0 {
		bad("audit.interval must not be negative")
	}
	if !(0 < a.Threshold && a.Threshold < 1) || a.Margin < 0 || a.Threshold+a.Margin > 1 {
		bad("audit: want 0 < threshold (%v) < threshold + margin (%v) <= 1", a.Threshold, a.Margin)
	}
	if a.MaxTokens <= 0 || a.Questions <= 0 || a.ScoreTokens <= 0 || a.CalibrateSamples < 10 {
		bad("audit: max_tokens, questions and score_tokens must be positive, calibrate_samples at least 10")
	}
	if a.DivFails <= 0 || a.DivFails > a.Questions {
		bad("audit.div_fails %d: want 1..questions (%d)", a.DivFails, a.Questions)
	}
	if a.Spread < 0 || a.CalibrateEvery < 0 {
		bad("audit.spread and audit.calibrate_every must not be negative")
	}
	seen := map[string]bool{}
	for i, r := range f.References {
		if r.ID == "" || r.Upstream == "" || r.URL == "" {
			bad("references[%d] needs id, upstream and url", i)
		}
		if seen[r.ID] {
			bad("references: duplicate id %q", r.ID)
		}
		if r.Verify != "" && !slices.Contains(audit.VerifyKinds, r.Verify) {
			bad("references[%d].verify %q: want one of %s", i, r.Verify, strings.Join(audit.VerifyKinds, ", "))
		}
		seen[r.ID] = true
	}
	return errors.Join(errs...)
}

func parseReferenceFlags(specs []string) ([]Reference, error) {
	var out []Reference
	for _, spec := range specs {
		kv, err := cli.KV(spec, "id", "upstream", "url", "key-env", "verify")
		if err != nil {
			return nil, err
		}
		if kv["id"] == "" || kv["upstream"] == "" || kv["url"] == "" {
			return nil, fmt.Errorf("%q needs id=, upstream= and url=", spec)
		}
		out = append(out, Reference{ID: kv["id"], Upstream: kv["upstream"], URL: kv["url"], APIKeyEnv: kv["key-env"], Verify: kv["verify"]})
	}
	return out, nil
}

func openDB(f File) (*sql.DB, error) {
	pw, err := config.Secret(f.Database.PasswordEnv, f.Database.PasswordFile, func(m string) { log.Print("WARNING: " + m) })
	if err != nil {
		return nil, fmt.Errorf("database password: %w", err)
	}
	return mysqldb.OpenWith(f.Database.DSN, mysqldb.Options{Password: pw, MaxOpenConns: f.Database.MaxOpenConns,
		MaxIdleConns: f.Database.MaxIdleConns, ConnMaxLifetime: f.Database.ConnMaxLifetime.T(),
		ConnectTimeout: f.Database.ConnectTimeout.T(), LockWaitTimeout: f.Database.LockWaitTimeout.T()})
}

func main() {
	f, args, err := load(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if err := f.validate(); err != nil {
		log.Fatalf("configuration:\n%v", err)
	}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	if cmd == "version" {
		fmt.Println(version.String("coordinator"))
		return
	}
	if cmd == "check-config" {
		_ = yaml.NewEncoder(os.Stdout).Encode(f)
		return
	}
	lg, closeLog, err := f.Log.Setup()
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()
	if cmd == "" || cmd == "serve" {
		log.Print(version.String("coordinator"))
	}
	if f.Debug.PprofListen != "" && (cmd == "" || cmd == "serve") {
		go func() { log.Printf("pprof: %v", http.ListenAndServe(f.Debug.PprofListen, nil)) }()
	}

	keys, err := p2p.LoadOrCreateKeys(f.Identity.KeyFile)
	if err != nil {
		log.Fatal(err)
	}
	if cmd == "id" { // relays need it for allow_peers before the coordinator first starts
		fmt.Println(keys.ID())
		return
	}
	db, err := openDB(f)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if cmd != "" && cmd != "serve" {
		if err := admin(f, keys, db, cmd, args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	j := f.Jobs
	cfg := coordinator.Config{DB: db, FeeBps: f.Market.FeeBps, DevCreditMicros: f.Dev.CreditMicros, OpenAccounts: f.Dev.OpenAccounts,
		Relays: f.Network.Relays, RelaySync: f.Network.RelaySync.T(), MinBudgetMicros: f.Market.MinBudgetMicros, WarmUp: f.Market.WarmUp.T(),
		TicketTTL: j.TicketTTL.T(), StartGrace: j.StartGrace.T(), ReconnectGrace: j.ReconnectGrace.T(),
		MaxStaleTickets: j.MaxStaleTickets, StaleAfter: j.StaleAfter.T(), SettleGrace: j.SettleGrace.T(),
		DisputeWindow: j.DisputeWindow.T(), MaxOfferTTL: j.MaxOfferTTL.T(), JobRetention: j.JobRetention.T(), SuspendFor: j.SuspendFor.T(),
		Log: lg}
	references, servers := buildReferences(f.References)
	if len(servers) > 0 {
		cfg.Counter = &tokens.Reference{Servers: servers}
	}
	if f.TEE.PolicyFile != "" {
		p, err := attest.LoadPolicy(f.TEE.PolicyFile)
		if err != nil {
			log.Fatal(err)
		}
		cfg.Attestation = &coordinator.Attestation{Verifier: cli.TEEVerifier(f.TEE.OnlineCheck), Policy: p}
	}

	h, err := p2p.NewHost(keys, p2p.HostConfig{Program: "coordinator", Listen: f.Network.Listen, DialOnly: len(f.Network.Listen) == 0, MaxConns: f.Network.MaxConns})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Startup writes to the shared database under the write lock; if the portal
	// (or an admin command) holds it longer than the lock wait timeout, retry
	// for a while instead of exiting.
	var svc *coordinator.Service
	for deadline := time.Now().Add(f.Database.StartupBusyRetry.T()); ; {
		svc, err = coordinator.NewService(ctx, h, keys, cfg)
		if err == nil || !mysqldb.IsBusy(err) || time.Now().After(deadline) {
			break
		}
		log.Printf("database busy at startup (%v); retrying", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	if len(references) > 0 && f.Audit.Interval > 0 {
		aud, err := newAuditor(f, svc.Engine, references, peer.AddrInfo{ID: h.ID()},
			func() []ma.Multiaddr { return append(h.Addrs(), svc.RelayAddrs()...) }, svc.OfficialRelays)
		if err != nil {
			log.Fatal(err)
		}
		if f.Audit.CalibrateEvery > 0 {
			go aud.CalibrateLoop(ctx, f.Audit.CalibrateEvery.T(), audit.CalibrateOptions{Samples: f.Audit.CalibrateSamples})
		}
		go aud.Run(ctx)
	}

	if f.Dev.CreditMicros > 0 || f.Dev.OpenAccounts {
		log.Print("WARNING: dev.credit_micros / dev.open_accounts let any new peer ID trade; never use them on a public network")
	}
	fmt.Println("coordinator", keys.ID())
	for _, a := range p2p.FullAddrs(h) {
		fmt.Println("   listening (private)", a)
	}
	// Report the relay addresses once the first reservations are in.
	go func() {
		for i := 0; i < 30 && ctx.Err() == nil; i++ {
			if addrs := svc.CircuitAddrs(); len(addrs) > 0 {
				for _, a := range addrs {
					fmt.Println("   via relay", a)
				}
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		log.Print("WARNING: no relay reservation yet: register relays with `portal relay add` or pass -relay; users cannot reach this coordinator until one succeeds")
	}()
	audits := "audits off"
	if f.Audit.Interval > 0 {
		audits = "audits every ~" + f.Audit.Interval.String()
		if f.Audit.Shadow {
			audits += " in shadow mode"
		}
	}
	for _, r := range references {
		method := audit.MethodNone
		if r.Verifier != nil {
			method = r.Verifier.Method()
		}
		fmt.Printf("   reference %s -> %s (%s, verify %s; exact token counts for disputes)\n", r.Model, r.UpstreamModel, audits, method)
	}
	<-ctx.Done()
}

func buildReferences(rs []Reference) ([]audit.Reference, map[string]tokens.Server) {
	var refs []audit.Reference
	servers := make(map[string]tokens.Server)
	for _, r := range rs {
		apiKey, err := config.Secret(r.APIKeyEnv, r.APIKeyFile, func(m string) { log.Printf("WARNING: references %s: %s", r.ID, m) })
		if err != nil {
			log.Fatalf("references %s: API key: %v", r.ID, err)
		}
		if apiKey == "" && r.APIKeyEnv != "" {
			log.Printf("WARNING: references %s: $%s is empty", r.ID, r.APIKeyEnv)
		}
		verifyURL := r.VerifyURL
		if verifyURL == "" {
			verifyURL = r.URL
		}
		v, err := audit.NewVerifier(r.Verify, verifyURL, apiKey, r.Upstream, r.Params)
		if err != nil {
			log.Fatalf("references %s: %v", r.ID, err)
		}
		refs = append(refs, audit.Reference{
			Model: r.ID, UpstreamModel: r.Upstream,
			Upstream: &node.OpenAICompat{BaseURL: r.URL, APIKey: apiKey},
			Verifier: v, Params: r.Params,
		})
		servers[r.ID] = tokens.Server{BaseURL: r.URL, Model: r.Upstream, APIKey: apiKey}
	}
	return refs, servers
}

// newAuditor builds the auditor from the audit settings.
func newAuditor(f File, eng *coordinator.Engine, refs []audit.Reference, coord peer.AddrInfo,
	coordAddrs func() []ma.Multiaddr, official func() []peer.ID) (*audit.Auditor, error) {
	a := f.Audit
	acfg := audit.Config{
		References: refs, Coordinator: coord, CoordinatorAddrs: coordAddrs, OfficialRelays: official,
		Interval: a.Interval.T(), BudgetMicros: a.BudgetMicros, MaxTokens: a.MaxTokens, Questions: a.Questions,
		Spread: a.Spread.T(), ScoreTokens: a.ScoreTokens, Threshold: a.Threshold, Margin: a.Margin, DivFails: a.DivFails,
		Shadow: a.Shadow,
	}
	if a.PromptsFile != "" {
		data, err := os.ReadFile(a.PromptsFile)
		if err != nil {
			return nil, err
		}
		if acfg.Prompts, err = audit.ParsePrompts(string(data)); err != nil {
			return nil, fmt.Errorf("%s: %w", a.PromptsFile, err)
		}
	}
	return audit.New(eng, acfg)
}

// admin runs an operator command against the database. It is safe beside a
// running coordinator: the write lock serializes the two processes' transactions.
func admin(f File, keys *p2p.Keys, db *sql.DB, cmd string, args []string) error {
	eng, err := coordinator.NewEngine(keys.ID(), keys.Signer, coordinator.Config{DB: db})
	if err != nil {
		return err
	}
	defer eng.Close()
	need := func(n int, usage string) error {
		if len(args) != n {
			return fmt.Errorf("usage: coordinator %s %s", cmd, usage)
		}
		return nil
	}
	switch cmd {
	case "balance":
		if err := need(1, "<peer-id>"); err != nil {
			return err
		}
		if _, err := peer.Decode(args[0]); err != nil {
			return fmt.Errorf("bad peer id: %w", err)
		}
		acct, err := eng.AccountOf(args[0])
		if err != nil {
			return err
		}
		b, err := eng.Balance(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s (%s) available %d held %d\n", args[0], acct, b.AvailableMicros, b.HeldMicros)
	case "relays":
		rs, err := accounts.Relays(db, false)
		if err != nil {
			return err
		}
		return printJSON(rs)
	case "resolve":
		if err := need(2, "<job-id> pay|refund"); err != nil {
			return err
		}
		if args[1] != "pay" && args[1] != "refund" {
			return errors.New("decision must be pay or refund")
		}
		if err := eng.ResolveDispute(args[0], args[1] == "pay"); err != nil {
			return err
		}
		st, _ := eng.JobStatus(args[0])
		fmt.Println(args[0], st)
	case "disputes":
		ds, err := eng.Disputes(100)
		if err != nil {
			return err
		}
		return printJSON(ds)
	case "nodes":
		ns, err := eng.Nodes()
		if err != nil {
			return err
		}
		return printJSON(ns)
	case "audits":
		as, err := eng.Audits(100)
		if err != nil {
			return err
		}
		return printJSON(as)
	case "audit":
		return auditCmd(f, keys, db, eng, args)
	case "check":
		if err := eng.CheckBooks(); err != nil {
			return err
		}
		fmt.Println("books balance: every account replays from the journal and the total is 0")
	default:
		return fmt.Errorf("unknown command %q (want serve, balance, resolve, disputes, nodes, audits, audit, relays, check); credit users with the portal", cmd)
	}
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
