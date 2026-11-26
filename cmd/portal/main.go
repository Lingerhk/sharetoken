// Command portal runs sharetoken's user-management backend and its operator
// commands. It shares its database with the coordinator.
//
//	portal -config portal.yaml serve            # configs/portal.example.yaml; flags below override it
//	portal -db mysql://sharetoken@127.0.0.1/sharetoken serve [-listen :8092] [-open-registration] [-signup-credit 1000000]
//	                                            # HTTPS: -tls-cert/-tls-key, else a self-signed certificate
//	                                            # (fingerprint printed; clients pin it); plain HTTP only on
//	                                            # loopback or with -insecure-http
//	                                            # web pages: / (users) and /admin/ (operators)
//	portal useradd [-admin] <email>             # people sign in with a code mailed to the address
//	portal rename <username> <email>            # e.g. an old account without an address
//	portal login-code <email>                   # a sign-in code without the mail (10 minutes, once)
//	portal grant <username> <micros> [memo]     # negative micros deduct
//	portal users | user <username> | disable <username> | enable <username>
//	portal relay add <multiaddr> [name] [region] | relay ls | relay rm|enable|disable <id>
//	portal overview                             # totals, and a replay of the ledger journal
//	portal pass-key                             # public key relays verify passes with (portal_keys in relay.yaml)
//
// Operator commands work on the database directly and may run beside the
// live portal and coordinator.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	netmail "net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"

	"sharetoken/internal/accounts"
	"sharetoken/internal/cli"
	"sharetoken/internal/config"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/p2p"
	"sharetoken/internal/portal"
	"sharetoken/internal/relaypass"
	"sharetoken/internal/version"
)

// File is portal.yaml (configs/portal.example.yaml).
type File struct {
	Version  int             `yaml:"version"`
	Database config.Database `yaml:"database"`
	Keys     struct {
		PassKeyFile string `yaml:"pass_key_file"`
		MFAKeyFile  string `yaml:"mfa_key_file"`
	} `yaml:"keys"`
	HTTP struct {
		Listen            string          `yaml:"listen"`
		TrustedProxies    []string        `yaml:"trusted_proxies"` // reverse proxies on other machines whose X-Real-IP is believed
		ReadHeaderTimeout config.Duration `yaml:"read_header_timeout"`
		ReadTimeout       config.Duration `yaml:"read_timeout"`
		WriteTimeout      config.Duration `yaml:"write_timeout"`
		ShutdownTimeout   config.Duration `yaml:"shutdown_timeout"`
	} `yaml:"http"`
	TLS struct {
		Mode            string   `yaml:"mode"` // auto, files, self-signed, proxy, insecure
		CertFile        string   `yaml:"cert_file"`
		KeyFile         string   `yaml:"key_file"`
		SelfSignedDir   string   `yaml:"self_signed_dir"`
		SelfSignedHosts []string `yaml:"self_signed_hosts"`
	} `yaml:"tls"`
	Accounts struct {
		OpenRegistration   bool   `yaml:"open_registration"`
		SignupCreditMicros uint64 `yaml:"signup_credit_micros"`
		MFAIssuer          string `yaml:"mfa_issuer"` // name shown in authenticator apps (default ShareToken)
	} `yaml:"accounts"`
	// Mail is who the portal's mail comes from; cmd/mailserver delivers it (docs/mail.md).
	// Sign-in codes are mail: without a sender nobody can sign in.
	Mail struct {
		From    string `yaml:"from"`    // ShareToken <team@sharetoken.app>
		Support string `yaml:"support"` // ShareToken Support <support@sharetoken.app>
	} `yaml:"mail"`
	// Downloads: the client packages the portal offers at /downloads/ (scripts/release/publish-packages.sh puts them there).
	Downloads struct {
		Dir string `yaml:"dir"`
	} `yaml:"downloads"`
	// Blog: the articles of /blog/, one folder per article (internal/portal/blog.go). Empty: no blog.
	Blog struct {
		Dir string `yaml:"dir"`
	} `yaml:"blog"`
	// RelayMetrics: read each relay's /metrics over SSH for the admin dashboard
	// (on the relay, restrict the key to that one command in authorized_keys).
	RelayMetrics struct {
		KeyFile        string          `yaml:"key_file"`
		KnownHostsFile string          `yaml:"known_hosts_file"`
		Interval       config.Duration `yaml:"interval"`
		Timeout        config.Duration `yaml:"timeout"`
		Targets        []struct {
			Relay   string `yaml:"relay"`   // name in the relay registry
			SSH     string `yaml:"ssh"`     // user@host[:port]
			Command string `yaml:"command"` // default: curl of 127.0.0.1:8093/metrics
		} `yaml:"targets"`
	} `yaml:"relay_metrics"`
	// Alerts: administrators' alerts (codes being guessed) go to Pushover.
	Alerts struct {
		PushoverFile string `yaml:"pushover_file"` // PUSHOVER_TOKEN=… and PUSHOVER_USER=… lines
	} `yaml:"alerts"`
	// Admin: where the admin pages, the admin API and administrators' sign-ins
	// may come from. allow_from empty: anywhere.
	Admin struct {
		AllowFrom       []string        `yaml:"allow_from"`       // IP addresses, CIDR prefixes, host names (every A/AAAA address)
		ResolveInterval config.Duration `yaml:"resolve_interval"` // how often host names are resolved again (default 5m)
	} `yaml:"admin"`
	Sessions struct {
		LoginTTL      config.Duration `yaml:"login_ttl"`
		WebSessionTTL config.Duration `yaml:"web_session_ttl"`
		PassTTL       config.Duration `yaml:"pass_ttl"`
	} `yaml:"sessions"`
	Log config.Log `yaml:"log"`
}

func defaults() File {
	var f File
	f.Version = config.Version
	f.Database = config.DefaultDatabase("")
	f.Keys.PassKeyFile = "portal-pass.key"
	f.HTTP.Listen = ":8092"
	f.HTTP.ReadHeaderTimeout, f.HTTP.ReadTimeout, f.HTTP.WriteTimeout, f.HTTP.ShutdownTimeout = config.D("10s"), config.D("30s"), config.D("60s"), config.D("5s")
	f.TLS.Mode, f.TLS.SelfSignedDir = "auto", "portal-tls"
	f.Sessions.LoginTTL, f.Sessions.WebSessionTTL, f.Sessions.PassTTL = config.D("1440h"), config.D("360h"), config.D("1h")
	f.Log = config.DefaultLog()
	return f
}

func (f *File) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if f.Database.DSN == "" {
		bad("database.dsn is empty")
	}
	if f.Keys.PassKeyFile == "" {
		bad("keys.pass_key_file is empty")
	}
	if rm := f.RelayMetrics; len(rm.Targets) > 0 {
		if rm.KeyFile == "" || rm.KnownHostsFile == "" {
			bad("relay_metrics needs key_file and known_hosts_file")
		}
		for i, t := range rm.Targets {
			if t.Relay == "" || !strings.Contains(t.SSH, "@") {
				bad("relay_metrics.targets[%d]: relay and ssh (user@host[:port]) are required", i)
			}
		}
	}
	switch f.TLS.Mode {
	case "auto", "self-signed", "proxy", "insecure":
	case "files":
		if f.TLS.CertFile == "" {
			bad("tls.mode files needs tls.cert_file and tls.key_file")
		}
	default:
		bad("tls.mode %q: want auto, files, self-signed, proxy or insecure", f.TLS.Mode)
	}
	if (f.TLS.CertFile == "") != (f.TLS.KeyFile == "") {
		bad("tls.cert_file and tls.key_file go together")
	}
	for name, d := range map[string]config.Duration{"http.read_header_timeout": f.HTTP.ReadHeaderTimeout, "http.read_timeout": f.HTTP.ReadTimeout,
		"http.write_timeout": f.HTTP.WriteTimeout, "http.shutdown_timeout": f.HTTP.ShutdownTimeout, "sessions.login_ttl": f.Sessions.LoginTTL,
		"sessions.web_session_ttl": f.Sessions.WebSessionTTL, "sessions.pass_ttl": f.Sessions.PassTTL} {
		if d <= 0 {
			bad("%s must be positive", name)
		}
	}
	for name, a := range map[string]string{"mail.from": f.Mail.From, "mail.support": f.Mail.Support} {
		if a == "" {
			continue
		}
		if _, err := netmail.ParseAddress(a); err != nil {
			bad("%s: %v", name, err)
		}
	}
	if err := portal.ParseAdminAllow(f.Admin.AllowFrom); err != nil {
		bad("%v", err)
	}
	if f.Admin.ResolveInterval < 0 || (f.Admin.ResolveInterval > 0 && f.Admin.ResolveInterval < config.D("10s")) {
		bad("admin.resolve_interval %v: use at least 10s", f.Admin.ResolveInterval)
	}
	if f.Sessions.PassTTL > 0 && f.Sessions.PassTTL < config.D("5m") {
		bad("sessions.pass_ttl %v: clients renew passes ahead of expiry; use at least 5m", f.Sessions.PassTTL)
	}
	return errors.Join(errs...)
}

func main() {
	f := defaults()
	if path := config.ConfigPath(os.Args[1:]); path != "" {
		if err := config.Load(path, &f); err != nil {
			log.Fatal(err)
		}
		config.Resolve(config.Dir(path), &f.Database.DSN, &f.Database.PasswordFile, &f.Keys.PassKeyFile, &f.Keys.MFAKeyFile,
			&f.TLS.CertFile, &f.TLS.KeyFile, &f.TLS.SelfSignedDir, &f.Log.File)
	}
	flag.String("config", "", "YAML configuration file (configs/portal.example.yaml); flags given explicitly override it")
	flag.StringVar(&f.Database.DSN, "db", f.Database.DSN, "database shared with the coordinator: mysql://user@host:port/db (password from database.password_env/password_file, default $SHARETOKEN_DB_PASSWORD)")
	flag.StringVar(&f.Keys.PassKeyFile, "pass-key", f.Keys.PassKeyFile, "key that signs relay passes (created with mode 0600 if missing; relays trust its public key: `portal pass-key`)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: portal [-config portal.yaml] [-db file] <command>
commands: version, version-policy, serve, check-config, useradd, rename, login-code, grant, users, user, disable, enable, mfa-reset, relay, overview, pass-key`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cmd, args := flag.Arg(0), flag.Args()[1:]
	if cmd == "version" {
		fmt.Println(version.String("portal"))
		return
	}
	if cmd == "serve" {
		serveFlags(&f, args)
	}
	if err := f.validate(); err != nil {
		log.Fatalf("configuration:\n%v", err)
	}
	if cmd == "check-config" {
		_ = yaml.NewEncoder(os.Stdout).Encode(f)
		return
	}
	lg, closeLog, err := f.Log.Setup()
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()
	if cmd == "serve" {
		log.Print(version.String("portal"))
	}
	pw, err := config.Secret(f.Database.PasswordEnv, f.Database.PasswordFile, func(m string) { log.Print("WARNING: " + m) })
	if err != nil {
		log.Fatalf("database password: %v", err)
	}
	db, err := mysqldb.OpenWith(f.Database.DSN, mysqldb.Options{Password: pw, MaxOpenConns: f.Database.MaxOpenConns,
		MaxIdleConns: f.Database.MaxIdleConns, ConnMaxLifetime: f.Database.ConnMaxLifetime.T(),
		ConnectTimeout: f.Database.ConnectTimeout.T(), LockWaitTimeout: f.Database.LockWaitTimeout.T()})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	passKeys, err := p2p.LoadOrCreateKeys(f.Keys.PassKeyFile)
	if err != nil {
		log.Fatal(err)
	}
	passPub := relaypass.FormatKey(passKeys.Signer.Public().(ed25519.PublicKey))
	switch cmd {
	case "serve":
		mfaKey, err := loadMFAKey(f.Keys.MFAKeyFile)
		if err != nil {
			log.Fatal(err)
		}
		serve(f, db, passKeys.Signer, passPub, mfaKey, lg)
		return
	case "pass-key":
		fmt.Println(passPub)
		return
	}
	srv, err := portal.New(portal.Config{DB: db})
	if err != nil {
		log.Fatal(err)
	}
	if err := admin(srv, cmd, args); err != nil {
		log.Fatal(err)
	}
}

// serveFlags applies the serve command's flags over the file's settings.
func serveFlags(f *File, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.StringVar(&f.HTTP.Listen, "listen", f.HTTP.Listen, "listen address")
	fs.BoolVar(&f.Accounts.OpenRegistration, "open-registration", f.Accounts.OpenRegistration, "an address without an account gets one at its first sign-in (otherwise admins create users)")
	fs.Uint64Var(&f.Accounts.SignupCreditMicros, "signup-credit", f.Accounts.SignupCreditMicros, "micro-credits granted to each new user")
	(config.Flags{FlagSet: fs}).Duration(&f.Sessions.LoginTTL, "login-ttl", "lifetime of the tokens clients get at sign-in")
	fs.StringVar(&f.TLS.CertFile, "tls-cert", f.TLS.CertFile, "TLS certificate file, e.g. from Let's Encrypt (default: a self-signed certificate that clients pin)")
	fs.StringVar(&f.TLS.KeyFile, "tls-key", f.TLS.KeyFile, "TLS key file for -tls-cert")
	fs.StringVar(&f.TLS.SelfSignedDir, "tls-dir", f.TLS.SelfSignedDir, "where the self-signed certificate is kept (keep it: clients pin its fingerprint)")
	(config.Flags{FlagSet: fs}).List(&f.TLS.SelfSignedHosts, "tls-hosts", "comma-separated host names / IPs for the self-signed certificate (informational: clients pin it)")
	(config.Flags{FlagSet: fs}).Duration(&f.Sessions.PassTTL, "pass-ttl", "lifetime of relay passes (disabling a user takes effect on relays within this)")
	insecure := fs.Bool("insecure-http", false, "same as tls.mode insecure: plain HTTP on a non-loopback address (test networks only)")
	(config.Flags{FlagSet: fs}).Duration(&f.Sessions.WebSessionTTL, "session-ttl", "lifetime of browser sessions on the web pages")
	behindTLS := fs.Bool("behind-tls-proxy", false, "same as tls.mode proxy: a reverse proxy terminates TLS in front of the portal")
	_ = fs.Parse(args)
	if *insecure {
		f.TLS.Mode = "insecure"
	}
	if *behindTLS {
		f.TLS.Mode = "proxy"
	}
}

func relayMetricsConfig(f File) portal.RelayMetricsConfig {
	rm := f.RelayMetrics
	c := portal.RelayMetricsConfig{KeyFile: rm.KeyFile, KnownHostsFile: rm.KnownHostsFile, Interval: rm.Interval.T(), Timeout: rm.Timeout.T()}
	for _, t := range rm.Targets {
		c.Targets = append(c.Targets, portal.RelayMetricsTarget{Relay: t.Relay, SSH: t.SSH, Command: t.Command})
	}
	return c
}

func loadMFAKey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil // derived from the pass key
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("keys.mfa_key_file: %w (create one with: head -c 32 /dev/urandom | xxd -p -c 64 > %s; chmod 600 it)", err, path)
	}
	if k, err := hex.DecodeString(strings.TrimSpace(string(data))); err == nil && len(k) == 32 {
		return k, nil
	}
	if len(data) == 32 {
		return data, nil
	}
	return nil, fmt.Errorf("keys.mfa_key_file %s: want 64 hex characters (32 bytes)", path)
}

func serve(f File, db *sql.DB, passKey ed25519.PrivateKey, passPub string, mfaKey []byte, lg *slog.Logger) {
	listen := f.HTTP.Listen
	host, _, _ := net.SplitHostPort(listen)
	loopback := cli.IsLoopback(host)
	mode := f.TLS.Mode
	if mode == "auto" {
		switch {
		case f.TLS.CertFile != "":
			mode = "files"
		case loopback:
			mode = "http"
		default:
			mode = "self-signed"
		}
	}
	p, err := portal.New(portal.Config{DB: db, OpenRegistration: f.Accounts.OpenRegistration, SignupCreditMicros: f.Accounts.SignupCreditMicros,
		LoginTTL: f.Sessions.LoginTTL.T(), PassKey: passKey, PassTTL: f.Sessions.PassTTL.T(), WebSessionTTL: f.Sessions.WebSessionTTL.T(),
		SecureCookies: mode == "proxy", TrustedProxies: trustedProxies(f.HTTP.TrustedProxies), MFAKey: mfaKey, Log: lg,
		Mail: portal.MailConfig{From: f.Mail.From, Support: f.Mail.Support}, DownloadsDir: f.Downloads.Dir, BlogDir: f.Blog.Dir,
		MFAIssuer: f.Accounts.MFAIssuer, RelayMetrics: relayMetricsConfig(f), PushoverFile: f.Alerts.PushoverFile,
		AdminAllow: portal.AdminAllowConfig{From: f.Admin.AllowFrom, Refresh: f.Admin.ResolveInterval.T()}})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go p.Run(ctx)
	hs := &http.Server{Addr: listen, Handler: p.Handler(), ReadHeaderTimeout: f.HTTP.ReadHeaderTimeout.T(),
		ReadTimeout: f.HTTP.ReadTimeout.T(), WriteTimeout: f.HTTP.WriteTimeout.T()}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), f.HTTP.ShutdownTimeout.T())
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()

	scheme := "https"
	switch mode {
	case "files":
		c, err := tls.LoadX509KeyPair(f.TLS.CertFile, f.TLS.KeyFile)
		if err != nil {
			log.Fatal(err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}
	case "http", "proxy", "insecure":
		scheme = "http"
		if mode == "insecure" && !loopback {
			log.Print("WARNING: tls.mode insecure: passwords and tokens cross the network unencrypted; test networks only")
		}
		if mode == "proxy" && !loopback {
			log.Print("note: tls.mode proxy: serving plain HTTP for a TLS-terminating proxy; do not expose this port directly")
		}
	case "self-signed":
		hosts := f.TLS.SelfSignedHosts
		if len(hosts) == 0 && host != "" {
			hosts = []string{host}
		}
		c, err := portal.SelfSignedCert(f.TLS.SelfSignedDir, hosts)
		if err != nil {
			log.Fatal(err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}
		fmt.Printf("self-signed certificate %s\n   fingerprint %s\n   users: SHARETOKEN_PORTAL=https://<host>%s sharetoken login -portal-pin %s\n",
			filepath.Join(f.TLS.SelfSignedDir, portal.SelfSignedCertFile), portal.CertFingerprint(c.Certificate[0]), portPart(listen), portal.CertFingerprint(c.Certificate[0]))
	}
	fmt.Printf("relay pass key %s\n   relays: access.portal_keys: [%s]\n", passPub, passPub)
	fmt.Printf("portal listening on %s://%s (tls mode %s, registration open: %v, sign-up credit: %d)\n   web: %s://<host>%s/app/  admin: %s://<host>%s/admin/\n",
		scheme, listen, mode, f.Accounts.OpenRegistration, f.Accounts.SignupCreditMicros, scheme, portPart(listen), scheme, portPart(listen))
	if scheme == "https" {
		err = hs.ListenAndServeTLS("", "")
	} else {
		err = hs.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func portPart(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil {
		return ":" + port
	}
	return ""
}

func admin(p *portal.Server, cmd string, args []string) error {
	st := p.Store()
	need := func(min int, usage string) error {
		if len(args) < min {
			return fmt.Errorf("usage: portal %s %s", cmd, usage)
		}
		return nil
	}
	user := func(name string) (int64, error) {
		u, err := st.UserByName(name)
		if err != nil {
			return 0, fmt.Errorf("user %q: %w", name, err)
		}
		return u.ID, nil
	}
	switch cmd {
	case "version-policy":
		cur, err := accounts.LoadVersionPolicy(st.DB)
		if err != nil {
			return err
		}
		if len(args) > 0 && args[0] == "set" {
			fs := flag.NewFlagSet("version-policy set", flag.ExitOnError)
			fs.StringVar(&cur.MinClient, "min", cur.MinClient, "older clients are refused new business")
			fs.StringVar(&cur.RecommendedClient, "recommended", cur.RecommendedClient, "older clients are told to upgrade")
			fs.StringVar(&cur.LatestClient, "latest", cur.LatestClient, "the newest release")
			fs.StringVar(&cur.DownloadURL, "download-url", cur.DownloadURL, "where to get it")
			fs.BoolVar(&cur.AllowDev, "allow-dev", cur.AllowDev, "serve builds that are not releases")
			_ = fs.Parse(args[1:])
			if err := accounts.SaveVersionPolicy(st.DB, cur); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(cur)
	case "useradd":
		fs := flag.NewFlagSet("useradd", flag.ExitOnError)
		isAdmin := fs.Bool("admin", false, "grant admin rights")
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: portal useradd [-admin] <email>")
		}
		u, err := p.CreateUser(fs.Arg(0), *isAdmin)
		if err != nil {
			return err
		}
		p.AuditCLI("user.create", u.Username, map[string]any{"is_admin": *isAdmin})
		return printJSON(u)
	case "rename":
		if err := need(2, "<username> <email>"); err != nil {
			return err
		}
		id, err := user(args[0])
		if err != nil {
			return err
		}
		u, err := st.RenameUser(id, args[1])
		if err != nil {
			return err
		}
		p.AuditCLI("user.rename", u.Username, map[string]any{"from": args[0]})
		return printJSON(u)
	case "login-code":
		// For someone whose mail does not arrive: the code a mail would carry,
		// printed here. It is as good as access to the mailbox; it is audited.
		if err := need(1, "<email>"); err != nil {
			return err
		}
		code, exp, err := p.IssueSignInCode(args[0])
		if errors.Is(err, accounts.ErrNotFound) {
			return fmt.Errorf("%s has no account (create it with: portal useradd %s)", args[0], args[0])
		}
		if err != nil {
			return err
		}
		p.AuditCLI("user.login_code", accounts.NormalizeEmail(args[0]), nil)
		fmt.Printf("%s\nsigns in %s on the web pages or in a client, typed where the mailed code goes, once, until %s (an earlier code from this command no longer works; two-factor authentication still applies)\n",
			code, accounts.NormalizeEmail(args[0]), exp.Local().Format("15:04:05"))
		return nil
	case "grant":
		if err := need(2, "<username> <micros> [memo]"); err != nil {
			return err
		}
		id, err := user(args[0])
		if err != nil {
			return err
		}
		micros, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return err
		}
		if err := p.AdjustCredit(id, micros, strings.Join(args[2:], " ")); err != nil {
			return err
		}
		p.AuditCLI("user.credit", args[0], map[string]any{"micros": micros, "memo": strings.Join(args[2:], " ")})
		b, err := p.Balance(id)
		if err != nil {
			return err
		}
		return printJSON(b)
	case "users":
		us, err := st.Users(1000, 0)
		if err != nil {
			return err
		}
		return printJSON(us)
	case "user":
		if err := need(1, "<username>"); err != nil {
			return err
		}
		id, err := user(args[0])
		if err != nil {
			return err
		}
		u, _ := st.User(id)
		b, err := p.Balance(id)
		if err != nil {
			return err
		}
		ds, err := st.Devices(id)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"user": u, "balance": b, "devices": ds})
	case "disable", "enable":
		if err := need(1, "<username>"); err != nil {
			return err
		}
		id, err := user(args[0])
		if err != nil {
			return err
		}
		if err := st.SetDisabled(id, cmd == "disable"); err != nil {
			return err
		}
		p.AuditCLI("user."+cmd, args[0], nil)
		return nil
	case "mfa-reset":
		// Recovery for an administrator who lost their authenticator: the
		// secret is removed and every token revoked; the next login enrolls
		// a new one.
		if err := need(1, "<username>"); err != nil {
			return err
		}
		id, err := user(args[0])
		if err != nil {
			return err
		}
		if err := st.ClearMFA(id); err != nil {
			return err
		}
		n, err := st.RevokeAllTokens(id)
		if err != nil {
			return err
		}
		p.AuditCLI("user.mfa_reset", args[0], map[string]any{"tokens_revoked": n})
		fmt.Printf("two-factor secret cleared, %d tokens revoked; %s enrolls again at the next login\n", n, args[0])
		return nil
	case "relay":
		return relayCmd(p, args)
	case "overview":
		o, err := p.Overview()
		if err != nil {
			return err
		}
		return printJSON(o)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func relayCmd(p *portal.Server, args []string) error {
	st := p.Store()
	if len(args) == 0 {
		return errors.New("usage: portal relay add <multiaddr> [name] [region] | ls | rm|enable|disable <id>")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			return errors.New("usage: portal relay add <multiaddr> [name] [region]")
		}
		get := func(i int) string {
			if len(args) > i {
				return args[i]
			}
			return ""
		}
		r, err := st.AddRelay(args[1], get(2), get(3))
		if err != nil {
			return err
		}
		return printJSON(r)
	case "ls":
		rs, err := st.Relays(false)
		if err != nil {
			return err
		}
		return printJSON(rs)
	case "rm", "enable", "disable":
		if len(args) != 2 {
			return fmt.Errorf("usage: portal relay %s <id>", args[0])
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return err
		}
		if args[0] == "rm" {
			return st.RemoveRelay(id)
		}
		return st.SetRelayEnabled(id, args[0] == "enable")
	}
	return fmt.Errorf("unknown relay command %q", args[0])
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// trustedProxies parses http.trusted_proxies, which validation has checked.
func trustedProxies(addrs []string) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, net.ParseIP(a))
	}
	return out
}
