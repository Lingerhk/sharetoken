package portal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The self-signed certificate is created once and reused, so the
// fingerprint clients pinned stays valid across restarts.
func TestSelfSignedCertIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := SelfSignedCert(dir, []string{"portal.example", "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := SelfSignedCert(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if CertFingerprint(a.Certificate[0]) != CertFingerprint(b.Certificate[0]) {
		t.Fatal("certificate changed across restarts")
	}
	if fi, err := os.Stat(filepath.Join(dir, SelfSignedKeyFile)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("key file mode: %v %v", fi.Mode(), err)
	}
}
