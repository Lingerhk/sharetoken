package main

import (
	"strings"
	"testing"

	"sharetoken/internal/config"
	"sharetoken/internal/coordinator"
)

func TestExampleConfigLoads(t *testing.T) {
	f, args, err := load([]string{"-config", "../../configs/coordinator.example.yaml", "nodes"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.validate(); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "nodes" || f.Market.FeeBps != 500 || len(f.References) != 1 || f.Jobs.DisputeWindow != config.D("24h") {
		t.Fatalf("%+v %v", f, args)
	}
	// The sample documents the defaults: keep them in step with the engine's.
	if d := defaults(); f.Jobs != d.Jobs || f.Market != d.Market || f.Audit.MatchAt != d.Audit.MatchAt || f.Network.RelaySync != d.Network.RelaySync {
		t.Fatalf("sample differs from the defaults:\n%+v %+v\n%+v %+v", f.Jobs, f.Market, d.Jobs, d.Market)
	}
	if d, e := defaults(), coordinator.Defaults(); d.Jobs.StaleAfter.T() != e.StaleAfter || d.Jobs.MaxStaleTickets != e.MaxStaleTickets || d.Jobs.TicketTTL.T() != e.TicketTTL {
		t.Fatalf("defaults %+v differ from the engine's", d.Jobs)
	}
	if f.Database.DSN != "mysql://sharetoken@127.0.0.1:3306/sharetoken" {
		t.Fatalf("a DSN must not be resolved as a path: %s", f.Database.DSN)
	}
}

func TestDefaultsMatchFlagsOnlyMode(t *testing.T) {
	f, _, err := load([]string{"-db", "x.db", "-reference", "id=a,upstream=b,url=http://h/v1,key-env=K"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.validate(); err != nil {
		t.Fatal(err)
	}
	if f.Database.DSN != "x.db" || len(f.References) != 1 || f.References[0].APIKeyEnv != "K" || f.Jobs.TicketTTL != config.D("1m") {
		t.Fatalf("%+v", f)
	}
}

func TestValidate(t *testing.T) {
	f := defaults()
	f.Jobs.StartGrace = config.D("1h")
	f.Jobs.JobRetention = config.D("1h")
	f.Audit.MatchAt = 0.3
	f.Market.FeeBps = 20000
	err := f.validate()
	for _, want := range []string{"start_grace", "job_retention", "mismatch_below", "fee_bps"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %s in %v", want, err)
		}
	}
}
