// Command node shares a self-hosted open-weight model on the sharetoken
// network.
//
//	ollama serve &                 # or vLLM / llama.cpp / SGLang
//	node -coordinator /ip4/203.0.113.7/tcp/4001/p2p/12D3Koo... \
//	     -upstream http://127.0.0.1:11434/v1 \
//	     -model id=qwen2.5-7b-instruct,upstream=qwen2.5:7b,in=50000,out=100000
//
// Prices are micro-credits per million tokens. The node needs no open port:
// it reserves a relay slot on the coordinator and hole-punches when it can.
//
// In an AMD SEV-SNP or Intel TDX confidential VM, -tee auto generates the
// node's keys in guest memory and advertises a hardware quote binding them,
// so buyers can verify that only this VM image ever decrypts their prompts.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/attest"
	"sharetoken/attest/hw"
	"sharetoken/internal/cli"
	"sharetoken/node"
	"sharetoken/p2p"
)

type modelList []node.ModelConfig

func (m *modelList) String() string { return fmt.Sprint(*m) }

func (m *modelList) Set(s string) error {
	var mc node.ModelConfig
	for kv := range strings.SplitSeq(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q", kv)
		}
		var err error
		switch k {
		case "id":
			mc.ID = v
		case "upstream":
			mc.UpstreamModel = v
		case "in":
			mc.PriceInMicros, err = strconv.ParseUint(v, 10, 64)
		case "out":
			mc.PriceOutMicros, err = strconv.ParseUint(v, 10, 64)
		default:
			return fmt.Errorf("unknown model field %q (want id, upstream, in, out)", k)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	if mc.ID == "" || mc.UpstreamModel == "" {
		return errors.New("a model needs id= and upstream=")
	}
	*m = append(*m, mc)
	return nil
}

// attestKeys obtains a hardware quote binding the node's receipt and box keys
// and prints the measurement that buyers' policies must allow-list.
func attestKeys(backend string, keys *p2p.Keys) (*attest.Quote, error) {
	att, err := hw.New(backend)
	if err != nil {
		return nil, err
	}
	q, err := att.Attest(attest.ReportData(keys.ReceiptKey(), keys.Box.Public()))
	if err != nil {
		return nil, err
	}
	claims, err := cli.TEEVerifier(false).Verify(q)
	if err != nil {
		return nil, fmt.Errorf("own quote does not verify: %w", err)
	}
	if claims.Debug {
		log.Print("WARNING: this guest is debuggable; buyers' policies will reject it")
	}
	fmt.Printf("tee: %s measurement %s (%s)\n", q.Backend, hex.EncodeToString(claims.Measurement), claims.Detail)
	return q, nil
}

func main() {
	keyPath := flag.String("key", "node.key", "identity seed file (created with mode 0600 if missing)")
	listen := flag.String("listen", "/ip4/0.0.0.0/tcp/4002,/ip4/0.0.0.0/udp/4002/quic-v1", "comma-separated listen multiaddrs")
	coordAddr := flag.String("coordinator", "", "coordinator multiaddr ending in /p2p/<peer-id> (required)")
	upstream := flag.String("upstream", "http://127.0.0.1:11434/v1", "OpenAI-compatible base URL of your local model server")
	upstreamKeyEnv := flag.String("upstream-key-env", "", "name of an environment variable holding the local server's API key, if it needs one")
	concurrency := flag.Int("concurrency", 2, "maximum simultaneous jobs")
	nat := flag.Bool("nat", true, "UPnP/NAT-PMP port mapping, AutoNAT and hole punching (disable for local testing)")
	useRelay := flag.Bool("relay", true, "reserve a relay slot on the coordinator so buyers can reach this node behind NAT")
	tee := flag.String("tee", "off", "attest this node from a confidential VM: off, auto, sev-snp or tdx")
	var models modelList
	flag.Var(&models, "model", `model to serve (repeatable): id=<marketplace id>,upstream=<server model name>,in=<price>,out=<price>`)
	flag.Parse()

	if *coordAddr == "" || len(models) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	coord, err := peer.AddrInfoFromString(*coordAddr)
	if err != nil {
		log.Fatalf("bad -coordinator: %v", err)
	}
	if u, err := url.Parse(*upstream); err == nil && u.Scheme == "http" && !cli.IsLocalURL(*upstream) {
		log.Printf("WARNING: prompts travel unencrypted to %s; use https, or keep the model server on this machine or a private network", *upstream)
	}

	var (
		keys  *p2p.Keys
		quote *attest.Quote
	)
	if *tee == "off" {
		if keys, err = p2p.LoadOrCreateKeys(*keyPath); err != nil {
			log.Fatal(err)
		}
	} else {
		// Keys exist only in guest memory: a key file would be readable by
		// whoever controls the disk. The peer ID therefore changes per boot.
		if keys, err = p2p.NewKeys(); err != nil {
			log.Fatal(err)
		}
		if quote, err = attestKeys(*tee, keys); err != nil {
			log.Fatal(err)
		}
		if u, err := url.Parse(*upstream); err != nil || !cli.IsLoopback(u.Hostname()) {
			log.Printf("WARNING: in TEE mode the model server must run inside this VM; prompts sent to %s leave the attested boundary", *upstream)
		}
	}
	hcfg := p2p.HostConfig{Listen: cli.List(*listen), NAT: *nat}
	if *useRelay {
		hcfg.StaticRelays = []peer.AddrInfo{*coord}
	}
	h, err := p2p.NewHost(keys, hcfg)
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var apiKey string
	if *upstreamKeyEnv != "" {
		apiKey = os.Getenv(*upstreamKeyEnv)
	}
	svc, err := node.NewService(ctx, h, keys, node.Config{
		Coordinator:    *coord,
		Models:         models,
		Upstream:       &node.OpenAICompat{BaseURL: *upstream, APIKey: apiKey},
		MaxConcurrency: *concurrency,
		Quote:          quote,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	fmt.Println("node", keys.ID())
	if quote != nil {
		fmt.Printf("   attested with %s\n", quote.Backend)
	}
	for _, m := range models {
		fmt.Printf("   serving %s (upstream %s) at in=%d out=%d micros/Mtok\n", m.ID, m.UpstreamModel, m.PriceInMicros, m.PriceOutMicros)
	}
	<-ctx.Done()
}
