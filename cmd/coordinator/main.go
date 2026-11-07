// Command coordinator runs the sharetoken matching, settlement, dispute and
// audit service for an open-weight model network.
//
//	coordinator -public                                  # serve (on a host with a public IP)
//	coordinator -reference id=qwen2.5-7b,upstream=qwen2.5:7b,url=http://127.0.0.1:11434/v1
//	                                                     # + audit nodes serving qwen2.5-7b
//	coordinator deposit <peer-id> <micros>               # operator commands, run beside
//	coordinator balance <peer-id>                        # the live service on its database
//	coordinator disputes | nodes | audits | check
//	coordinator resolve <job-id> pay|refund
//
// It prints its peer ID and addresses; give the /p2p/ address to nodes and
// buyers as -coordinator.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

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
	dbPath := flag.String("db", "coordinator.db", `SQLite database for the ledger, jobs and node records (":memory:" keeps nothing)`)
	listen := flag.String("listen", "/ip4/0.0.0.0/tcp/4001,/ip4/0.0.0.0/udp/4001/quic-v1", "comma-separated listen multiaddrs")
	feeBps := flag.Int64("fee-bps", 500, "platform fee, in basis points of each settled job")
	public := flag.Bool("public", false, "this host has a public IP: also act as a circuit relay for nodes behind NAT")
	nat := flag.Bool("nat", true, "UPnP/NAT-PMP port mapping, AutoNAT and hole punching (disable for local testing)")
	devCredit := flag.Uint64("dev-credit", 0, "DEV ONLY: credit each new peer this many micro-credits on first use")
	var refs cli.Repeated
	flag.Var(&refs, "reference", "reference copy of a model (repeatable): id=<marketplace id>,upstream=<server model name>,url=<OpenAI-compatible base URL>[,key-env=<VAR>]; enables audits and exact token counts for disputes")
	auditEvery := flag.Duration("audit-interval", 10*time.Minute, "mean time between audits of nodes serving a referenced model (0 disables)")
	auditBudget := flag.Uint64("audit-budget", 200_000, "micro-credits reserved per audit job, paid from platform fees")
	auditPrompts := flag.String("audit-prompts", "", "file of challenge prompts, one per line (default: randomized built-ins)")
	teePolicy := flag.String("tee-policy", "", "JSON allow-list of TEE images; enables require_tee matching (see attest.LoadPolicy)")
	teeOnline := flag.Bool("tee-online", false, "also check TEE revocations / TCB status with AMD and Intel (network)")
	flag.Parse()

	keys, err := p2p.LoadOrCreateKeys(*keyPath)
	if err != nil {
		log.Fatal(err)
	}
	db, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if cmd := flag.Arg(0); cmd != "" && cmd != "serve" {
		if err := admin(keys, db, cmd, flag.Args()[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg := coordinator.Config{DB: db, FeeBps: *feeBps, DevCreditMicros: *devCredit}
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

	h, err := p2p.NewHost(keys, p2p.HostConfig{Listen: cli.List(*listen), NAT: *nat, RelayService: *public, ForcePublic: *public})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	svc, err := coordinator.NewService(ctx, h, keys, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	if len(references) > 0 && *auditEvery > 0 {
		acfg := audit.Config{
			References:   references,
			Coordinator:  peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()},
			Interval:     *auditEvery,
			BudgetMicros: *auditBudget,
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

	if *devCredit > 0 {
		log.Print("WARNING: -dev-credit hands free credits to any new peer ID; never use it on a public network")
	}
	fmt.Println("coordinator", keys.ID())
	for _, a := range p2p.FullAddrs(h) {
		fmt.Println("  ", a)
	}
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
	case "deposit":
		if err := need(2, "<peer-id> <micros>"); err != nil {
			return err
		}
		micros, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return err
		}
		if _, err := peer.Decode(args[0]); err != nil {
			return fmt.Errorf("bad peer id: %w", err)
		}
		if err := eng.Deposit(args[0], micros); err != nil {
			return err
		}
		fallthrough
	case "balance":
		if len(args) < 1 {
			return errors.New("usage: coordinator balance <peer-id>")
		}
		b, err := eng.Balance(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s available %d held %d\n", args[0], b.AvailableMicros, b.HeldMicros)
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
		return fmt.Errorf("unknown command %q (want serve, deposit, balance, resolve, disputes, nodes, audits, check)", cmd)
	}
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
