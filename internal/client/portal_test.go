package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
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
	p, err := portal.New(portal.Config{DB: db, OpenRegistration: true})
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
	if _, err := NewPortalClient(srv.URL, "", fp).Register(ctx, "alice", "correct horse", "t"); err != nil {
		t.Fatalf("pinned client: %v", err)
	}
	if _, err := NewPortalClient(srv.URL, "", "").Login(ctx, "alice", "correct horse", "t"); err == nil {
		t.Fatal("an unpinned client must not trust a self-signed certificate")
	}
	other, _ := portal.SelfSignedCert(t.TempDir(), nil)
	if _, err := NewPortalClient(srv.URL, "", portal.CertFingerprint(other.Certificate[0])).Login(ctx, "alice", "correct horse", "t"); err == nil {
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
