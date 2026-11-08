// Command coordinator runs the sharetoken accounting platform: matching,
// tickets, settlement, disputes and audits for an open-weight model network.
//
// It needs no public address. It holds a reservation on every relay (the
// enabled relays registered in the database through the portal, plus any
// -relay), and users reach it only through those relays. It shares its
// database with the portal, which manages users, devices and credits.
//
//	coordinator -db sharetoken.db                        # serve
//	coordinator -reference id=qwen2.5-7b,upstream=qwen2.5:7b,url=http://127.0.0.1:11434/v1
//	                                                     # + audit nodes serving qwen2.5-7b
//	coordinator balance <peer-id>                        # operator commands, run beside
//	coordinator disputes | nodes | audits | relays | check   # the live service on its database
//	coordinator resolve <job-id> pay|refund
//	coordinator id                                       # peer ID, for relays' -allow-peer
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
	_ "net/http/pprof" // served only with -debug
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/accounts"
	"sharetoken/attest"
	"sharetoken/audit"
	"sharetoken/coordinator"
	"sharetoken/internal/cli"
	"sharetoken/internal/sqlite"
	"sharetoken/node"
	"sharetoken/p2p"
	"sharetoken/tokens"
)

func main() {
	keyPath := flag.String("key", "coordinator.key", "identity seed file (created with mode 0600 if missing)")
	dbPath := flag.String("db", "sharetoken.db", `database shared with the portal (ledger, accounts, jobs, node records): a SQLite file, or mysql://user@host:port/db (password from $SHARETOKEN_DB_PASSWORD)`)
	listen := flag.String("listen", "", "comma-separated listen multiaddrs (default: none; the coordinator is reachable only through its relays)")
	feeBps := flag.Int64("fee-bps", 500, "platform fee, in basis points of each settled job")
	var relays cli.Repeated
	flag.Var(&relays, "relay", "relay to reserve on, as a full multiaddr ending in /p2p/<id> (repeatable), besides those registered in the database")
	maxConns := flag.Int("max-conns", 8192, "connection-manager high watermark (every online user holds a relayed connection)")
	openAccounts := flag.Bool("open-accounts", false, "DEV ONLY: let peers that are not bound to a user trade from their own account")
	devCredit := flag.Uint64("dev-credit", 0, "DEV ONLY: credit each new peer this many micro-credits on first use")
	var refs cli.Repeated
	flag.Var(&refs, "reference", "reference copy of a model (repeatable): id=<marketplace id>,upstream=<server model name>,url=<OpenAI-compatible base URL>[,key-env=<VAR>]; enables audits and exact token counts for disputes")
	auditEvery := flag.Duration("audit-interval", 10*time.Minute, "mean time between audits of nodes serving a referenced model (0 disables)")
	auditBudget := flag.Uint64("audit-budget", 200_000, "micro-credits reserved per audit job, paid from platform fees")
	auditPrompts := flag.String("audit-prompts", "", "file of challenge prompts, one per line (default: randomized built-ins)")
	teePolicy := flag.String("tee-policy", "", "JSON allow-list of TEE images; enables require_tee matching (see attest.LoadPolicy)")
	teeOnline := flag.Bool("tee-online", false, "also check TEE revocations / TCB status with AMD and Intel (network)")
	debugAddr := flag.String("debug", "", "serve Go pprof profiles at http://<addr>/debug/pprof/ (keep it on loopback), e.g. 127.0.0.1:6060")
	flag.Parse()
	if *debugAddr != "" {
		go func() { log.Printf("pprof: %v", http.ListenAndServe(*debugAddr, nil)) }()
	}

	keys, err := p2p.LoadOrCreateKeys(*keyPath)
	if err != nil {
		log.Fatal(err)
	}
	if flag.Arg(0) == "id" { // relays need it for -allow-peer before the coordinator first starts
		fmt.Println(keys.ID())
		return
	}
	db, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if flag.Arg(0) == "copy-db" { // before the admin engine: it must not write the source
		if err := copyDB(db, flag.Args()[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if cmd := flag.Arg(0); cmd != "" && cmd != "serve" {
		if err := admin(keys, db, cmd, flag.Args()[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg := coordinator.Config{DB: db, FeeBps: *feeBps, DevCreditMicros: *devCredit, OpenAccounts: *openAccounts, Relays: relays}
	references, servers, err := parseReferences(refs)
	if err != nil {
		log.Fatalf("-reference: %v", err)
	}
	if len(servers) > 0 {
		cfg.Counter = &tokens.Reference{Servers: servers}
	}
	if *teePolicy != "" {
		p, err := attest.LoadPolicy(*teePolicy)
		if err != nil {
			log.Fatal(err)
		}
		cfg.Attestation = &coordinator.Attestation{Verifier: cli.TEEVerifier(*teeOnline), Policy: p}
	}

	h, err := p2p.NewHost(keys, p2p.HostConfig{Listen: cli.List(*listen), DialOnly: *listen == "", MaxConns: *maxConns})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Startup migrates the shared database under a write lock; if the portal
	// (or an admin command) holds it longer than the busy timeout, retry for
	// a while instead of exiting.
	var svc *coordinator.Service
	for deadline := time.Now().Add(2 * time.Minute); ; {
		svc, err = coordinator.NewService(ctx, h, keys, cfg)
		if err == nil || !sqlite.IsBusy(err) || time.Now().After(deadline) {
			break
		}
		log.Printf("database busy at startup (%v); retrying", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	if len(references) > 0 && *auditEvery > 0 {
		acfg := audit.Config{
			References:       references,
			Coordinator:      peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()},
			CoordinatorAddrs: func() []ma.Multiaddr { return append(h.Addrs(), svc.RelayAddrs()...) },
			Interval:         *auditEvery,
			BudgetMicros:     *auditBudget,
		}
		if *auditPrompts != "" {
			data, err := os.ReadFile(*auditPrompts)
			if err != nil {
				log.Fatal(err)
			}
			acfg.Prompts = cli.Lines(string(data))
		}
		aud, err := audit.New(svc.Engine, acfg)
		if err != nil {
			log.Fatal(err)
		}
		go aud.Run(ctx)
	}

	if *devCredit > 0 || *openAccounts {
		log.Print("WARNING: -dev-credit / -open-accounts let any new peer ID trade; never use them on a public network")
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
	for _, r := range references {
		fmt.Printf("   reference %s -> %s (audits every ~%s, exact token counts for disputes)\n", r.Model, r.UpstreamModel, *auditEvery)
	}
	<-ctx.Done()
}

func parseReferences(specs []string) ([]audit.Reference, map[string]tokens.Server, error) {
	var refs []audit.Reference
	servers := make(map[string]tokens.Server)
	for _, spec := range specs {
		kv, err := cli.KV(spec, "id", "upstream", "url", "key-env")
		if err != nil {
			return nil, nil, err
		}
		if kv["id"] == "" || kv["upstream"] == "" || kv["url"] == "" {
			return nil, nil, fmt.Errorf("%q needs id=, upstream= and url=", spec)
		}
		apiKey := ""
		if kv["key-env"] != "" {
			apiKey = os.Getenv(kv["key-env"])
		}
		refs = append(refs, audit.Reference{
			Model: kv["id"], UpstreamModel: kv["upstream"],
			Upstream: &node.OpenAICompat{BaseURL: kv["url"], APIKey: apiKey},
		})
		servers[kv["id"]] = tokens.Server{BaseURL: kv["url"], Model: kv["upstream"], APIKey: apiKey}
	}
	return refs, servers, nil
}

// admin runs an operator command against the database. It is safe beside a
// running coordinator: SQLite serializes the two processes' transactions.
// copyDB copies the -db database into another one, normally an empty MySQL
// database: coordinator -db st.db copy-db mysql://sharetoken@host/sharetoken.
// Stop the portal and the coordinator first.
func copyDB(src *sql.DB, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: coordinator -db <source> copy-db <destination DSN or file>")
	}
	dst, err := sqlite.Open(args[0])
	if err != nil {
		return err
	}
	defer dst.Close()
	start := time.Now()
	if err := coordinator.CopyDatabase(src, dst, func(t string, n int64) { fmt.Printf("%-16s %d rows\n", t, n) }); err != nil {
		return err
	}
	fmt.Printf("copied in %v; check it with: coordinator -db <destination> check\n", time.Since(start).Round(time.Millisecond))
	return nil
}

func admin(keys *p2p.Keys, db *sql.DB, cmd string, args []string) error {
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
	case "check":
		if err := eng.CheckBooks(); err != nil {
			return err
		}
		fmt.Println("books balance: every account replays from the journal and the total is 0")
	default:
		return fmt.Errorf("unknown command %q (want serve, balance, resolve, disputes, nodes, audits, relays, check); credit users with the portal", cmd)
	}
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
