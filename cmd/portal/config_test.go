package main

import (
	"strings"
	"testing"

	"sharetoken/internal/config"
)

func TestExampleConfigLoads(t *testing.T) {
	f := defaults()
	if err := config.Load("../../configs/portal.example.yaml", &f); err != nil {
		t.Fatal(err)
	}
	if err := f.validate(); err != nil {
		t.Fatal(err)
	}
	if f.TLS.Mode != "auto" || f.Sessions.LoginTTL != config.D("1440h") || f.Sessions.WebSessionTTL != config.D("360h") || f.HTTP.Listen != ":8092" {
		t.Fatalf("%+v", f)
	}
}

func TestServeFlags(t *testing.T) {
	f := defaults()
	serveFlags(&f, []string{"-listen", "127.0.0.1:1", "-open-registration", "-behind-tls-proxy", "-pass-ttl", "30m"})
	if f.HTTP.Listen != "127.0.0.1:1" || !f.Accounts.OpenRegistration || f.TLS.Mode != "proxy" || f.Sessions.PassTTL != config.D("30m") {
		t.Fatalf("%+v", f)
	}
	f.TLS.Mode = "https"
	f.TLS.CertFile = "x"
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "tls.mode") || !strings.Contains(err.Error(), "go together") {
		t.Fatal(err)
	}
}

func TestMFAKey(t *testing.T) {
	if k, err := loadMFAKey(""); k != nil || err != nil {
		t.Fatal(k, err)
	}
	if _, err := loadMFAKey("/nonexistent/k"); err == nil || !strings.Contains(err.Error(), "urandom") {
		t.Fatal(err)
	}
}
