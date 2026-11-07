// Command buyer is the marketplace client.
//
//	buyer -coordinator <addr>            # local OpenAI-compatible endpoint at http://127.0.0.1:9787/v1
//	buyer -coordinator <addr> models     # list models with live offers
//	buyer -coordinator <addr> balance    # show this peer's credits
//
// Point any OpenAI-compatible tool at http://127.0.0.1:9787/v1 with a model ID
// from "models". Each request reserves -budget micro-credits; the unused part
// is refunded at settlement.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/attest"
	"sharetoken/buyer"
	"sharetoken/internal/cli"
	"sharetoken/p2p"
)

func main() {
	keyPath := flag.String("key", "buyer.key", "identity seed file (created with mode 0600 if missing)")
	coordAddr := flag.String("coordinator", "", "coordinator multiaddr ending in /p2p/<peer-id> (required)")
	httpAddr := flag.String("http", "127.0.0.1:9787", "listen address of the local OpenAI-compatible endpoint (keep it on loopback)")
	budget := flag.Uint64("budget", 200_000, "micro-credits reserved per request")
	listen := flag.String("listen", "/ip4/0.0.0.0/tcp/0,/ip4/0.0.0.0/udp/0/quic-v1", "comma-separated listen multiaddrs (ephemeral ports help hole punching)")
	nat := flag.Bool("nat", true, "UPnP/NAT-PMP port mapping, AutoNAT and hole punching (disable for local testing)")
	maxIn := flag.Uint64("max-price-in", 0, "refuse nodes charging more per million prompt tokens (0 = no limit)")
	maxOut := flag.Uint64("max-price-out", 0, "refuse nodes charging more per million completion tokens (0 = no limit)")
	evidence := flag.Bool("evidence", true, "when a node's receipt is provably wrong, disclose that one job's key and transcript to the coordinator so it can refund you automatically")
	requireTEE := flag.Bool("require-tee", false, "use only nodes in a confidential VM whose quote passes -tee-policy")
	teePolicy := flag.String("tee-policy", "", "JSON allow-list of trusted TEE images (see attest.LoadPolicy)")
	teeOnline := flag.Bool("tee-online", false, "also check TEE revocations / TCB status with AMD and Intel (network)")
	flag.Parse()

	if *coordAddr == "" {
		flag.Usage()
		os.Exit(2)
	}
	coord, err := peer.AddrInfoFromString(*coordAddr)
	if err != nil {
		log.Fatalf("bad -coordinator: %v", err)
	}
	keys, err := p2p.LoadOrCreateKeys(*keyPath)
	if err != nil {
		log.Fatal(err)
	}
	h, err := p2p.NewHost(keys, p2p.HostConfig{Listen: cli.List(*listen), NAT: *nat})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = h.Connect(cctx, *coord)
	cancel()
	if err != nil {
		log.Fatalf("cannot reach coordinator: %v", err)
	}
	cl := buyer.New(h, keys, *coord)
	cl.Evidence = *evidence
	if *teePolicy != "" {
		p, err := attest.LoadPolicy(*teePolicy)
		if err != nil {
			log.Fatal(err)
		}
		cl.Attestation = &buyer.Attestation{Verifier: cli.TEEVerifier(*teeOnline), Policy: p}
	} else if *requireTEE {
		log.Fatal("-require-tee needs -tee-policy: the list of node images you trust")
	}

	switch cmd := flag.Arg(0); cmd {
	case "", "proxy":
		if host, _, err := net.SplitHostPort(*httpAddr); err == nil && !cli.IsLocalHost(host) {
			log.Printf("WARNING: %s is reachable from other machines; anyone who can reach it can spend your credits", *httpAddr)
		}
		proxy := &buyer.Proxy{Client: cl, BudgetMicros: *budget, MaxPriceIn: *maxIn, MaxPriceOut: *maxOut, RequireTEE: *requireTEE}
		srv := &http.Server{Addr: *httpAddr, Handler: proxy.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			_ = srv.Shutdown(context.Background())
		}()
		fmt.Printf("buyer %s\n   OpenAI-compatible endpoint: http://%s/v1\n", keys.ID(), *httpAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
		// Let disputes still in flight reach the coordinator.
		wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = cl.WaitDisputes(wctx)
		cancel()
	case "models":
		ms, err := cl.Models(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-32s %6s %6s %14s %14s\n", "MODEL", "NODES", "TEE", "MIN IN/Mtok", "MIN OUT/Mtok")
		for _, m := range ms {
			fmt.Printf("%-32s %6d %6d %14d %14d\n", m.Model, m.Nodes, m.AttestedNodes, m.MinPriceIn, m.MinPriceOut)
		}
	case "balance":
		b, err := cl.Balance(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s\n   available %d micro-credits, on hold %d\n", keys.ID(), b.AvailableMicros, b.HeldMicros)
	default:
		log.Fatalf("unknown command %q (want proxy, models or balance)", cmd)
	}
}
