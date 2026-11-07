package portal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Files of the portal's self-signed certificate.
const (
	SelfSignedCertFile = "portal-cert.pem"
	SelfSignedKeyFile  = "portal-key.pem"
)

// CertFingerprint is how clients pin a certificate: "sha256:" + hex of the
// SHA-256 of its DER encoding.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SelfSignedCert loads the portal's self-signed certificate from dir, or
// creates one (ECDSA P-256, valid 10 years, key file mode 0600). It is meant
// for deployments without a domain certificate: clients pin its fingerprint
// on first login, the way SSH remembers host keys. Keep dir: a new
// certificate has a new fingerprint and every client would have to re-pin.
func SelfSignedCert(dir string, hosts []string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, SelfSignedCertFile), filepath.Join(dir, SelfSignedKeyFile)
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("portal: %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "sharetoken portal"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}
