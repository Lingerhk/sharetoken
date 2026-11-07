// Package cli holds small helpers shared by the command binaries.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"sharetoken/attest"
	"sharetoken/attest/sevsnp"
	"sharetoken/attest/tdx"
)

// List splits a comma-separated flag value, dropping empty items.
func List(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsLocalHost reports whether host is loopback or a private-network address.
func IsLocalHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// IsLoopback reports whether host is this machine.
func IsLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// IsLocalURL reports whether rawURL points at a loopback or private host.
func IsLocalURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && IsLocalHost(u.Hostname())
}

// KV parses "k1=v1,k2=v2" into a map, accepting only the given keys.
func KV(s string, keys ...string) (map[string]string, error) {
	out := make(map[string]string)
	for kv := range strings.SplitSeq(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			return nil, fmt.Errorf("expected key=value, got %q", kv)
		}
		if !slices.Contains(keys, k) {
			return nil, fmt.Errorf("unknown field %q (want %s)", k, strings.Join(keys, ", "))
		}
		out[k] = v
	}
	return out, nil
}

// Repeated is a flag that may be given several times.
type Repeated []string

func (r *Repeated) String() string     { return strings.Join(*r, " ") }
func (r *Repeated) Set(v string) error { *r = append(*r, v); return nil }

// TEEVerifier verifies AMD SEV-SNP and Intel TDX quotes against the vendors'
// roots. online adds revocation checks (and, for TDX, TCB status from Intel
// PCS); offline verification checks the signature chains only.
func TEEVerifier(online bool) attest.Verifier {
	return attest.Multi{
		sevsnp.Backend: &sevsnp.Verifier{CheckRevocations: online},
		tdx.Backend:    &tdx.Verifier{GetCollateral: online, CheckRevocations: online},
	}
}

// Lines splits text into trimmed lines, dropping blanks and # comments.
func Lines(text string) []string {
	var out []string
	for l := range strings.Lines(text) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// ReadPassword returns $SHARETOKEN_PASSWORD if set; otherwise it prompts on
// the terminal without echo (asking twice when confirm is set), or reads one
// line from stdin when stdin is not a terminal.
func ReadPassword(prompt string, confirm bool) (string, error) {
	if p := os.Getenv("SHARETOKEN_PASSWORD"); p != "" {
		return p, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	read := func(p string) (string, error) {
		fmt.Fprint(os.Stderr, p)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	pw, err := read(prompt)
	if err != nil {
		return "", err
	}
	if confirm {
		again, err := read("Repeat password: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("passwords do not match")
		}
	}
	return pw, nil
}

// Prompt asks for one line of input on the terminal.
func Prompt(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
