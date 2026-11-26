package official

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The Codex Desktop unlock (after ferry's codex-unlock). Codex Desktop
// locks its input once the ChatGPT plan is spent, even when the model
// picked is a network model. With the unlock on, Codex's config points
// chatgpt_base_url at this gateway (/backend-api/…, sharetoken connect), and
// the gateway passes those requests to the ChatGPT backend as they are,
// except that
//
//   - the usage answers (wham/usage) say the limit is not reached, so the
//     input stays open: official models still fail at the backend, network
//     models go on;
//   - the account check points the workspace's backend at the local HTTPS
//     server (UnlockPort, a certificate this user trusts for localhost
//     only), so the Desktop's own requests come here as well;
//   - the Desktop's MCP requests that carry no login get Codex's own
//     (~/.codex/auth.json), when they show the client's API key:
//     chatgpt_base_url carries it in its path (/k/<key>/backend-api/).
//     Loopback is open to every program and user of the machine, and
//     without the key any of them could act as the user at ChatGPT.
//
// While it is on, Codex Desktop needs the client running, and the Codex
// guard is off (a held model is what the lock was).

const (
	backendPrefix  = "/backend-api"
	unlockCertFile = "unlock-cert.pem"
	unlockKeyFile  = "unlock-key.pem"
	// UnlockCertPrefix starts the certificate's common name.
	UnlockCertPrefix = "ShareToken localhost"
)

// UnlockPort is the local HTTPS server's port (443 unless
// SHARETOKEN_UNLOCK_PORT says otherwise).
func UnlockPort() int {
	if p, err := strconv.Atoi(os.Getenv("SHARETOKEN_UNLOCK_PORT")); err == nil && p > 0 {
		return p
	}
	return 443
}

func unlockOrigin() string {
	if p := UnlockPort(); p != 443 {
		return fmt.Sprintf("https://localhost:%d", p)
	}
	return "https://localhost"
}

// IsBackendPath reports whether p is a ChatGPT backend path the gateway
// passes on.
func IsBackendPath(p string) bool {
	p = path.Clean("/" + p)
	return p == backendPrefix || strings.HasPrefix(p, backendPrefix+"/")
}

// ServeBackend passes a /backend-api request to the ChatGPT backend.
func (g *Gateway) ServeBackend(w http.ResponseWriter, r *http.Request) { g.serveBackend(w, r, false) }

func (g *Gateway) serveBackend(w http.ResponseWriter, r *http.Request, fromTLS bool) {
	clean := path.Clean("/" + r.URL.Path)
	if !IsBackendPath(clean) {
		http.NotFound(w, r)
		return
	}
	sub := strings.Trim(strings.TrimPrefix(clean, backendPrefix), "/")
	target := g.up.ChatGPTBackend + "/" + sub
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	unlock := g.Settings().CodexUnlock
	isUsage := unlock && (sub == "wham/usage" || sub == "wham/usage/subscriptions")
	isCheck := unlock && !fromTLS && sub == "wham/accounts/check"
	h := r.Header.Clone()
	for _, k := range append([]string{"Host", "Accept-Encoding", "Content-Length"}, hopHeaders...) {
		h.Del(k)
	}
	if unlock && h.Get("Authorization") == "" && (sub == "ps/mcp" || strings.HasPrefix(sub, "ps/mcp/")) {
		if strings.Contains(sub, "%") || strings.Contains(sub, "..") {
			// What the backend might read as another path (%2e%2e
			// here was %252e%252e) is not passed on at all.
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		// Codex's login only for a caller that showed the API key, on
		// this machine, and on a plain path; anyone else's request goes
		// on as it came.
		if HasKey(r.Context()) && fromLoopback(r) && plainSubPath(sub) {
			if tok, acct := codexCLIAuth(); tok != "" {
				h.Set("Authorization", "Bearer "+tok)
				if acct != "" && h.Get("chatgpt-account-id") == "" {
					h.Set("chatgpt-account-id", acct)
				}
			}
		}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Header, req.ContentLength = h, r.ContentLength
	resp, err := g.hc.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			g.log.Warn("ChatGPT backend unreachable", "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Could not reach ChatGPT: " + err.Error()})
		}
		return
	}
	defer resp.Body.Close()
	if (!isUsage && !isCheck) || resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		copyResponse(w, resp)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) == nil {
		changed := false
		if isUsage {
			changed = unlockUsage(doc)
			if changed {
				g.log.Info("Codex Desktop: a spent plan's usage answered as open (unlock)")
			}
		} else {
			changed = pointAccountsAt(doc, unlockOrigin())
		}
		if changed {
			body, _ = json.Marshal(doc)
		}
	}
	writeBuffered(w, resp, body)
}

// plainSubPath: letters, digits and - . _ ~ / only, and no "..": nothing
// that makes another path or a query of it on the way.
func plainSubPath(sub string) bool {
	for _, c := range sub {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.ContainsRune("-._~/", c):
		default:
			return false
		}
	}
	return !strings.Contains(sub, "..")
}

// fromLoopback reports whether r came from this machine to a loopback
// address of it: `sharetoken up -http` may listen elsewhere.
func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && !a.IP.IsLoopback() {
		return false
	}
	return true
}

// unlockUsage makes a usage answer say the limit is not reached; false when
// it did not say so (or the account has credits to go on with anyway).
func unlockUsage(u map[string]any) bool {
	rl, _ := u["rate_limit"].(map[string]any)
	sc, _ := u["spend_control"].(map[string]any)
	reached := u["rate_limit_reached_type"] != nil ||
		(rl != nil && (rl["allowed"] == false || rl["limit_reached"] == true)) ||
		(sc != nil && sc["reached"] == true)
	if !reached {
		return false
	}
	if c, _ := u["credits"].(map[string]any); c != nil && (c["has_credits"] == true || c["unlimited"] == true) {
		return false
	}
	if rl != nil {
		rl["allowed"], rl["limit_reached"] = true, false
	}
	if _, ok := u["rate_limit_reached_type"]; ok {
		u["rate_limit_reached_type"] = nil
	}
	if sc != nil && sc["reached"] == true {
		sc["reached"] = false
	}
	return true
}

// pointAccountsAt sets the backend of the accounts that may use any to
// origin.
func pointAccountsAt(doc map[string]any, origin string) bool {
	accts, _ := doc["accounts"].([]any)
	changed := false
	for _, a := range accts {
		if m, _ := a.(map[string]any); m != nil && m["workspace_backend_origin"] == "NO_CONSTRAINT" {
			m["workspace_backend_origin"] = origin
			changed = true
		}
	}
	return changed
}

// codexCLIAuth is Codex's own ChatGPT login ($CODEX_HOME/auth.json).
func codexCLIAuth() (token, account string) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		}
	}
	var a struct {
		Tokens struct {
			Access  string `json:"access_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || json.Unmarshal(data, &a) != nil {
		return "", ""
	}
	return a.Tokens.Access, a.Tokens.Account
}

// StartUnlock runs the local HTTPS server while the unlock is on, until ctx
// ends; it is started again by SetSettings.
func (g *Gateway) StartUnlock(ctx context.Context) {
	g.mu.Lock()
	g.ctx = ctx
	g.mu.Unlock()
	g.syncUnlock()
}

// syncUnlock starts or stops the HTTPS server as the settings say.
func (g *Gateway) syncUnlock() {
	g.mu.Lock()
	defer g.mu.Unlock()
	want := g.settings.CodexUnlock && g.ctx != nil && g.ctx.Err() == nil
	if !want {
		for _, s := range g.unlockSrv {
			_ = s.Close()
		}
		g.unlockSrv, g.unlockErr = nil, ""
		return
	}
	if len(g.unlockSrv) > 0 {
		return
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(g.home, unlockCertFile), filepath.Join(g.home, unlockKeyFile))
	if err != nil {
		g.unlockErr = "no certificate: run `sharetoken codex-unlock on`"
		g.log.Warn("Codex unlock: " + g.unlockErr)
		return
	}
	port := strconv.Itoa(UnlockPort())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host != "localhost" && host != "127.0.0.1" && host != "::1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" || !IsBackendPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		g.serveBackend(w, r, true)
	})
	var errs []string
	for _, addr := range []string{"127.0.0.1:" + port, "[::1]:" + port} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			ErrorLog:  nil}
		g.unlockSrv = append(g.unlockSrv, srv)
		go func() { _ = srv.ServeTLS(ln, "", "") }()
	}
	if len(g.unlockSrv) == 0 {
		g.unlockErr = "cannot listen on port " + port + ": " + strings.Join(errs, "; ")
		g.log.Warn("Codex unlock: " + g.unlockErr)
		return
	}
	g.unlockErr = ""
	g.log.Info("Codex unlock: HTTPS server listening", "port", port)
	go func(ctx context.Context) {
		<-ctx.Done()
		g.syncUnlock()
	}(g.ctx)
}

// UnlockCertPath is where home keeps the unlock's certificate.
func UnlockCertPath(home string) string { return filepath.Join(home, unlockCertFile) }

// EnsureUnlockCert makes home's localhost certificate when there is none,
// or it ends within 60 days; made reports a new one (to be trusted).
func EnsureUnlockCert(home string) (certPath string, made bool, err error) {
	certPath = UnlockCertPath(home)
	if c, err := readCert(certPath); err == nil && time.Until(c.NotAfter) > 60*24*time.Hour {
		if _, err := os.Stat(filepath.Join(home, unlockKeyFile)); err == nil {
			return certPath, false, nil
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", false, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: UnlockCertPrefix + " " + hex.EncodeToString(suffix), Organization: []string{"ShareToken"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(800 * 24 * time.Hour), // under Apple's 825-day limit
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", false, err
	}
	old, _ := readCert(certPath)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(home, unlockKeyFile), keyPEM, 0o600); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", false, err
	}
	if old != nil {
		_ = untrustCert(old, certPath)
	}
	return certPath, true, nil
}

func readCert(p string) (*x509.Certificate, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

// TrustUnlockCert asks the system to trust home's certificate for TLS: on
// macOS in the login keychain, on Windows in the user's root store. Both
// ask the user to confirm.
func TrustUnlockCert(home string) error {
	p := UnlockCertPath(home)
	switch runtime.GOOS {
	case "darwin":
		kc, err := loginKeychain()
		if err != nil {
			return err
		}
		return run("security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", kc, p)
	case "windows":
		return run("certutil", "-user", "-addstore", "Root", p)
	}
	return errors.New("the Codex Desktop unlock is for macOS and Windows")
}

// UntrustUnlockCert removes home's certificate from the system's trust and
// deletes it.
func UntrustUnlockCert(home string) error {
	p := UnlockCertPath(home)
	c, err := readCert(p)
	if err != nil {
		return nil
	}
	err = untrustCert(c, p)
	_ = os.Remove(p)
	_ = os.Remove(filepath.Join(home, unlockKeyFile))
	return err
}

func untrustCert(c *x509.Certificate, p string) error {
	switch runtime.GOOS {
	case "darwin":
		tmp := filepath.Join(os.TempDir(), fmt.Sprintf("sharetoken-unlock-%d.pem", os.Getpid()))
		_ = os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}), 0o600)
		defer os.Remove(tmp)
		_ = run("security", "remove-trusted-cert", tmp)
		kc, err := loginKeychain()
		if err != nil {
			return err
		}
		sum := sha1.Sum(c.Raw)
		return run("security", "delete-certificate", "-Z", strings.ToUpper(hex.EncodeToString(sum[:])), kc)
	case "windows":
		return run("certutil", "-user", "-delstore", "Root", c.SerialNumber.Text(16))
	}
	return nil
}

// UnlockCertTrusted reports whether the system trusts home's certificate
// for localhost.
func UnlockCertTrusted(home string) bool {
	c, err := readCert(UnlockCertPath(home))
	if err != nil {
		return false
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	_, err = c.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: roots})
	return err == nil
}

func loginKeychain() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "Library", "Keychains", "login.keychain-db"), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
