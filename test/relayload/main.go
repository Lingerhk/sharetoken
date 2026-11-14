//go:build bench

// relayload measures what a sharetoken relay costs per client.
//
//	relayload relay -listen /ip4/0.0.0.0/tcp/4101,/ip4/0.0.0.0/udp/4101/quic-v1 -addrfile relay.addr
//	relayload load -relay <full addr> -idle N -pairs M -frame B -interval D -dur 60s
//
// The relay is the production relay package with open admission. "load"
// starts N idle clients (a reservation, a ping every 10 s, like real users)
// and M seller/buyer pairs: each seller holds a reservation, each buyer opens
// a stream through the relay circuit to its seller, and the seller writes a
// frame of B bytes every D (a token stream) until the end.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/p2p"
	"sharetoken/internal/relay"
)

const proto = "/bench/stream/1"

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("relay | load")
	}
	switch os.Args[1] {
	case "relay":
		runRelay(os.Args[2:])
	case "load":
		runLoad(os.Args[2:])
	}
}

func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	listen := fs.String("listen", "/ip4/0.0.0.0/tcp/4101,/ip4/0.0.0.0/udp/4101/quic-v1", "")
	addrfile := fs.String("addrfile", "relay.addr", "")
	_ = fs.Parse(args)
	if m := p2p.MachineMemory(); m > 0 { // as cmd/relay does
		debug.SetMemoryLimit(int64(m / 100 * 85))
	}
	k, _ := p2p.NewKeys()
	r, err := relay.New(k, relay.Config{Listen: strings.Split(*listen, ","), MaxReservationsPerIP: 100000, MaxReservations: 100000})
	if err != nil {
		log.Fatal(err)
	}
	_ = os.WriteFile(*addrfile, []byte(strings.Join(r.Addrs(), "\n")+"\n"), 0o644)
	log.Printf("relay up: %v open=%v pid=%d", r.Addrs(), r.Open(), os.Getpid())
	for {
		time.Sleep(10 * time.Second)
		s := r.Stats()
		log.Printf("reservations=%d circuits=%d bytes_relayed=%d", s.Reservations, s.Circuits, s.BytesRelayed)
	}
}

func newClient() host.Host {
	cm, _ := connmgr.NewConnManager(10, 20)
	h, err := libp2p.New(libp2p.NoListenAddrs, libp2p.EnableRelay(), libp2p.ConnectionManager(cm),
		libp2p.ResourceManager(&network.NullResourceManager{}), libp2p.DisableMetrics())
	if err != nil {
		log.Fatal(err)
	}
	return h
}

func runLoad(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	raddr := fs.String("relay", "", "relay multiaddr with /p2p/")
	idle := fs.Int("idle", 0, "")
	pairs := fs.Int("pairs", 0, "")
	frame := fs.Int("frame", 55, "bytes per frame")
	interval := fs.Duration("interval", 20*time.Millisecond, "")
	dur := fs.Duration("dur", 60*time.Second, "")
	_ = fs.Parse(args)
	m, err := ma.NewMultiaddr(*raddr)
	if err != nil {
		log.Fatal(err)
	}
	ri, _ := peer.AddrInfoFromP2pAddr(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reserve := func(h host.Host) bool {
		for try := 0; try < 5; try++ {
			c, cc := context.WithTimeout(ctx, 20*time.Second)
			err := h.Connect(c, *ri)
			if err == nil {
				_, err = client.Reserve(c, h, *ri)
			}
			cc()
			if err == nil {
				return true
			}
			time.Sleep(time.Second)
		}
		return false
	}
	pinger := func(h host.Host) {
		go func() {
			time.Sleep(time.Duration(rand.Int64N(int64(10 * time.Second))))
			for ctx.Err() == nil {
				c, cc := context.WithTimeout(ctx, 5*time.Second)
				<-ping.Ping(c, h, ri.ID)
				cc()
				time.Sleep(10 * time.Second)
			}
		}()
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	var okIdle atomic.Int64
	t0 := time.Now()
	for i := 0; i < *idle; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			h := newClient()
			if reserve(h) {
				okIdle.Add(1)
				pinger(h)
			}
		}()
	}
	wg.Wait()
	log.Printf("idle clients with reservations: %d/%d in %v", okIdle.Load(), *idle, time.Since(t0).Round(time.Millisecond))

	var received, streams atomic.Int64
	payload := make([]byte, *frame)
	type pair struct{ s, b host.Host }
	ps := make([]pair, *pairs)
	for i := range ps {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			s, b := newClient(), newClient()
			s.SetStreamHandler(proto, func(st network.Stream) {
				defer st.Close()
				tk := time.NewTicker(*interval)
				defer tk.Stop()
				for ctx.Err() == nil {
					<-tk.C
					if _, err := st.Write(payload); err != nil {
						return
					}
				}
			})
			if !reserve(s) {
				log.Printf("pair %d: no reservation", i)
				return
			}
			pinger(s)
			pinger(b)
			ps[i] = pair{s, b}
		}(i)
	}
	wg.Wait()
	for i, p := range ps {
		if p.s == nil {
			continue
		}
		circ, _ := ma.NewMultiaddr(fmt.Sprintf("%s/p2p-circuit/p2p/%s", *raddr, p.s.ID()))
		go func(i int, p pair) {
			c, cc := context.WithTimeout(ctx, 30*time.Second)
			defer cc()
			if err := p.b.Connect(c, peer.AddrInfo{ID: p.s.ID(), Addrs: []ma.Multiaddr{circ}}); err != nil {
				log.Printf("pair %d: connect: %v", i, err)
				return
			}
			st, err := p.b.NewStream(network.WithAllowLimitedConn(ctx, "bench"), p.s.ID(), proto)
			if err != nil {
				log.Printf("pair %d: stream: %v", i, err)
				return
			}
			streams.Add(1)
			n, _ := io.Copy(io.Discard, &countReader{st, &received})
			_ = n
		}(i, p)
		time.Sleep(min(time.Duration(int64(*interval)/int64(max(len(ps), 1))), 5*time.Millisecond))
	}
	time.Sleep(3 * time.Second)
	log.Printf("streams open: %d/%d; measuring %v", streams.Load(), *pairs, *dur)
	fmt.Println("READY")
	r0, s0 := received.Load(), time.Now()
	time.Sleep(*dur)
	got := received.Load() - r0
	want := float64(*pairs) * float64(*frame) * dur.Seconds() / interval.Seconds()
	log.Printf("delivered %.1f KB/s to buyers (%.0f%% of the %.1f KB/s sent), streams %d", float64(got)/time.Since(s0).Seconds()/1024, 100*float64(got)/want, want/dur.Seconds()/1024, streams.Load())
	fmt.Println("DONE")
}

type countReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}
