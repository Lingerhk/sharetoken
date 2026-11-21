package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/accounts"
	"sharetoken/internal/audit"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/p2p"
)

// auditCmd runs `coordinator audit calibrate|try|calibrations` beside the
// running coordinator.
func auditCmd(f File, keys *p2p.Keys, db *sql.DB, eng *coordinator.Engine, args []string) error {
	usage := errors.New("usage: coordinator audit calibrate [-samples n] [model] | try <node-peer-id> <model> | calibrations")
	if len(args) == 0 {
		return usage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	refs, _ := buildReferences(f.References)
	if len(refs) == 0 && args[0] != "calibrations" {
		return errors.New("no references configured")
	}
	switch args[0] {
	case "calibrations":
		cs, err := coordinator.QueryCalibrations(db, "", 100)
		if err != nil {
			return err
		}
		return printJSON(cs)
	case "calibrate":
		fs := flag.NewFlagSet("audit calibrate", flag.ExitOnError)
		samples := fs.Int("samples", f.Audit.CalibrateSamples, "questions to answer and score")
		_ = fs.Parse(args[1:])
		a, err := newAuditor(f, eng, nil, refs, peer.AddrInfo{ID: keys.PeerID}, nil, nil)
		if err != nil {
			return err
		}
		var done int
		for _, r := range refs {
			if fs.NArg() > 0 && fs.Arg(0) != r.Model {
				continue
			}
			if r.Verifier == nil || r.Verifier.Method() == audit.MethodNone {
				fmt.Printf("%s: verify none, nothing to calibrate\n", r.Model)
				continue
			}
			c, err := a.Calibrate(ctx, r.Model, audit.CalibrateOptions{Samples: *samples})
			if err != nil {
				return err
			}
			done++
			fmt.Printf("%s (%s): threshold %.3f, drift %v\n   %s\n", c.Model, c.Method, c.Threshold, c.Drift, c.Detail)
		}
		if done == 0 && fs.NArg() > 0 {
			return fmt.Errorf("no scoring reference %q", fs.Arg(0))
		}
		return nil
	case "try":
		if len(args) != 3 {
			return usage
		}
		if _, err := peer.Decode(args[1]); err != nil {
			return fmt.Errorf("bad node peer id: %w", err)
		}
		official, circuits, err := officialRelays(db, f.Network.Relays)
		if err != nil {
			return err
		}
		if len(official) == 0 {
			return errors.New("no official relay registered: audits reach nodes only through official relays")
		}
		g := f
		g.Audit.Shadow, g.Audit.Spread = true, 0 // an operator's check: record, never penalize
		a, err := newAuditor(g, eng, nil, refs, peer.AddrInfo{ID: keys.PeerID, Addrs: circuits}, nil,
			func() []peer.ID { return official })
		if err != nil {
			return err
		}
		rec, err := a.Audit(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		return printJSON(rec)
	}
	return usage
}

// officialRelays reads the relays the coordinator reserves on (registered and
// enabled, plus -relay) and the coordinator's circuit addresses through them.
func officialRelays(db *sql.DB, static []string) ([]peer.ID, []ma.Multiaddr, error) {
	rs, err := accounts.Relays(db, true)
	if err != nil {
		return nil, nil, err
	}
	addrs := static
	for _, r := range rs {
		addrs = append(addrs, r.Addr)
	}
	var ids []peer.ID
	var circuits []ma.Multiaddr
	circuit := ma.StringCast("/p2p-circuit")
	for _, a := range addrs {
		ai, err := p2p.ParseRelay(a)
		if err != nil {
			continue
		}
		ids = append(ids, ai.ID)
		for _, m := range ai.Addrs {
			p, _ := ma.NewComponent("p2p", ai.ID.String())
			circuits = append(circuits, m.Encapsulate(p.Multiaddr()).Encapsulate(circuit))
		}
	}
	return ids, circuits, nil
}
