package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sharetoken/internal/mysqldb"
	"sharetoken/internal/portal"
)

// A portal on HTTPS with a self-signed certificate: the client works only
// with the matching pin.
func TestPortalPinning(t *testing.T) {
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := portal.New(portal.Config{DB: db, OpenRegistration: true, Mail: portal.MailConfig{From: "ShareToken <team@example.test>"}})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := portal.SelfSignedCert(t.TempDir(), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(p.Handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	ctx := context.Background()
	fp := portal.CertFingerprint(cert.Certificate[0])

	pc, err := ProbePortal(ctx, srv.URL)
	if err != nil || pc.Fingerprint != fp || pc.Trusted {
		t.Fatalf("probe: %+v %v", pc, err)
	}
	if _, err := NewPortalClient(srv.URL, "", fp).RequestCode(ctx, "alice@example.test", ""); err != nil {
		t.Fatalf("pinned client: %v", err)
	}
	if _, err := NewPortalClient(srv.URL, "", "").RequestCode(ctx, "bob@example.test", ""); err == nil {
		t.Fatal("an unpinned client must not trust a self-signed certificate")
	}
	other, _ := portal.SelfSignedCert(t.TempDir(), nil)
	if _, err := NewPortalClient(srv.URL, "", portal.CertFingerprint(other.Certificate[0])).RequestCode(ctx, "carol@example.test", ""); err == nil {
		t.Fatal("a client pinned to another certificate must refuse (man in the middle)")
	}

	for u, want := range map[string]bool{
		"http://203.0.113.7:8092": true, "http://127.0.0.1:8092": false, "https://203.0.113.7:8092": false, "http://localhost:1": false,
	} {
		if InsecurePortalURL(u) != want {
			t.Errorf("InsecurePortalURL(%s) != %v", u, want)
		}
	}
	_ = http.StatusOK
}

// "login -send" keeps the nonce of its code for the "login -code" that
// follows: for that portal and address only, and no longer than the code lasts.
func TestPendingCode(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	const portalURL = "https://portal.example.test"
	if got := LoadPendingCode(home, portalURL, "a@example.test"); got != "" {
		t.Fatalf("a nonce from nowhere: %q", got)
	}
	if err := SavePendingCode(home, portalURL, " A@Example.test ", &CodeSent{Nonce: "n1", ExpiresInSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(home, pendingCodeFile)); err != nil || fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Fatalf("the nonce must be readable by its user only: %v %v", fi, err)
	}
	if got := LoadPendingCode(home, portalURL, "a@example.test"); got != "n1" {
		t.Fatalf("nonce %q", got)
	}
	if LoadPendingCode(home, portalURL, "b@example.test") != "" || LoadPendingCode(home, "https://other.example.test", "a@example.test") != "" {
		t.Fatal("a nonce was handed to another address or portal")
	}
	if err := SavePendingCode(home, portalURL, "a@example.test", &CodeSent{Nonce: "n2", ExpiresInSeconds: -1}); err != nil {
		t.Fatal(err)
	}
	if got := LoadPendingCode(home, portalURL, "a@example.test"); got != "" {
		t.Fatalf("the nonce of an expired code: %q", got)
	}
	ClearPendingCode(home)
	if _, err := os.Stat(filepath.Join(home, pendingCodeFile)); !os.IsNotExist(err) {
		t.Fatalf("not cleared: %v", err)
	}
}
