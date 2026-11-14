//go:build ignore

// Signs release packages with the release key, an Ed25519 key kept offline
// (default local/keys/release-signing.pem, or $ST_RELEASE_KEY). Clients install
// a package by themselves only if its signature checks out against the public
// key built into them (internal/client/updatekey.go, gui/updatekey.go).
//
// The signature is over the file's SHA-256 digest (Ed25519 of the 32 bytes),
// which is also what Wails' updater checks ("ed25519" with a sha256 digest).
//
//	go run scripts/release/sign-packages.go genkey <private.pem>          make a key; prints the public key
//	go run scripts/release/sign-packages.go pub <private.pem>             print the public key
//	go run scripts/release/sign-packages.go sign <private.pem> <SIGNATURES-file> <files...>
//	go run scripts/release/sign-packages.go verify <public-base64> <SIGNATURES-file> <files...>
//
// SIGNATURES-<version> has a line "<base64 signature>  <file name>" per file,
// the layout of SHA256SUMS; the portal reads both.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: sign-packages.go genkey|pub|sign|verify ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "genkey":
		err = genkey(os.Args[2])
	case "pub":
		var k ed25519.PrivateKey
		if k, err = readKey(os.Args[2]); err == nil {
			fmt.Println(base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey)))
		}
	case "sign":
		if len(os.Args) < 5 {
			err = errors.New("sign <private.pem> <SIGNATURES-file> <files...>")
			break
		}
		err = sign(os.Args[2], os.Args[3], os.Args[4:])
	case "verify":
		if len(os.Args) < 5 {
			err = errors.New("verify <public-base64> <SIGNATURES-file> <files...>")
			break
		}
		err = verify(os.Args[2], os.Args[3], os.Args[4:])
	default:
		err = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func genkey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, fmt.Errorf("%s: not PEM", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return ek, nil
}

func digest(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// sign adds the files' signatures to the SIGNATURES file, replacing older lines for them.
func sign(keyPath, out string, files []string) error {
	k, err := readKey(keyPath)
	if err != nil {
		return err
	}
	lines := readSigs(out)
	for _, f := range files {
		d, err := digest(f)
		if err != nil {
			return err
		}
		lines[filepath.Base(f)] = base64.StdEncoding.EncodeToString(ed25519.Sign(k, d))
	}
	names := make([]string, 0, len(lines))
	for n := range lines {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", lines[n], n)
	}
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

func readSigs(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) == 2 {
			out[fs[1]] = fs[0]
		}
	}
	return out
}

func verify(pubB64, sigFile string, files []string) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad public key")
	}
	sigs := readSigs(sigFile)
	for _, f := range files {
		sig, err := base64.StdEncoding.DecodeString(sigs[filepath.Base(f)])
		if err != nil || len(sig) == 0 {
			return fmt.Errorf("%s: no signature", f)
		}
		d, err := digest(f)
		if err != nil {
			return err
		}
		if !ed25519.Verify(pub, d, sig) {
			return fmt.Errorf("%s: signature does not verify", f)
		}
	}
	return nil
}
