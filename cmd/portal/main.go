// Command portal runs sharetoken's user-management backend and its operator
// commands. It shares its database with the coordinator.
//
//	portal [-db sharetoken.db] serve [-listen :8092] [-open-registration] [-signup-credit 1000000]
//	                                            # HTTPS: -tls-cert/-tls-key, else a self-signed certificate
//	                                            # (fingerprint printed; clients pin it); plain HTTP only on
//	                                            # loopback or with -insecure-http
//	                                            # web pages: / (users) and /admin/ (operators)
//	portal useradd [-admin] <username>          # password from $SHARETOKEN_PASSWORD or a prompt
//	portal passwd <username>
//	portal grant <username> <micros> [memo]     # negative micros deduct
//	portal users | user <username> | disable <username> | enable <username>
//	portal relay add <multiaddr> [name] [region] | relay ls | relay rm|enable|disable <id>
//	portal overview                             # totals, and a replay of the ledger journal
//	portal pass-key                             # public key relays verify passes with (relay -portal-key)
//
// Operator commands work on the database directly and may run beside the
// live portal and coordinator.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sharetoken/internal/cli"
	"sharetoken/internal/sqlite"
	"sharetoken/p2p"
	"sharetoken/portal"
	"sharetoken/relaypass"
)

func main() {
	dbPath := flag.String("db", "sharetoken.db", "database shared with the coordinator")
	passKeyPath := flag.String("pass-key", "portal-pass.key", "key that signs relay passes (created with mode 0600 if missing; relays trust its public key: `portal pass-key`)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: portal [-db file] <command>
commands: serve, useradd, passwd, grant, users, user, disable, enable, relay, overview, pass-key`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	db, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	cmd, args := flag.Arg(0), flag.Args()[1:]
	passKeys, err := p2p.LoadOrCreateKeys(*passKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	passPub := relaypass.FormatKey(passKeys.Signer.Public().(ed25519.PublicKey))
	switch cmd {
	case "serve":
		serve(db, passKeys.Signer, passPub, args)
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

func serve(db *sql.DB, passKey ed25519.PrivateKey, passPub string, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":8092", "listen address")
	open := fs.Bool("open-registration", false, "let anyone sign up (otherwise admins create users)")
	signup := fs.Uint64("signup-credit", 0, "micro-credits granted to each new user")
	loginTTL := fs.Duration("login-ttl", 30*24*time.Hour, "lifetime of tokens issued at login")
	cert := fs.String("tls-cert", "", "TLS certificate file, e.g. from Let's Encrypt (default: a self-signed certificate that clients pin)")
	key := fs.String("tls-key", "", "TLS key file for -tls-cert")
	tlsDir := fs.String("tls-dir", "portal-tls", "where the self-signed certificate is kept (keep it: clients pin its fingerprint)")
	tlsHosts := fs.String("tls-hosts", "", "comma-separated host names / IPs for the self-signed certificate (informational: clients pin it)")
	passTTL := fs.Duration("pass-ttl", time.Hour, "lifetime of relay passes (disabling a user takes effect on relays within this)")
	insecure := fs.Bool("insecure-http", false, "serve plain HTTP on a non-loopback address (test networks only: passwords and tokens travel in clear)")
	sessionTTL := fs.Duration("session-ttl", 24*time.Hour, "lifetime of browser sessions on the web pages")
	behindTLS := fs.Bool("behind-tls-proxy", false, "a reverse proxy terminates TLS in front of the portal: mark session cookies Secure anyway")
	_ = fs.Parse(args)
	if (*cert == "") != (*key == "") {
		log.Fatal("-tls-cert and -tls-key go together")
	}
	p, err := portal.New(portal.Config{DB: db, OpenRegistration: *open, SignupCreditMicros: *signup, LoginTTL: *loginTTL,
		PassKey: passKey, PassTTL: *passTTL, WebSessionTTL: *sessionTTL, SecureCookies: *behindTLS})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hs := &http.Server{Addr: *listen, Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()

	host, _, _ := net.SplitHostPort(*listen)
	loopback := cli.IsLoopback(host)
	mode := "https"
	switch {
	case *cert != "":
		c, err := tls.LoadX509KeyPair(*cert, *key)
		if err != nil {
			log.Fatal(err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}
	case *insecure || loopback:
		mode = "http"
		if !loopback {
			log.Print("WARNING: -insecure-http: passwords and tokens cross the network unencrypted; test networks only")
		}
	default:
		hosts := cli.List(*tlsHosts)
		if len(hosts) == 0 && host != "" {
			hosts = []string{host}
		}
		c, err := portal.SelfSignedCert(*tlsDir, hosts)
		if err != nil {
			log.Fatal(err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}
		fmt.Printf("self-signed certificate %s\n   fingerprint %s\n   users: sharetoken login -portal https://<host>%s -portal-pin %s\n",
			filepath.Join(*tlsDir, portal.SelfSignedCertFile), portal.CertFingerprint(c.Certificate[0]), portPart(*listen), portal.CertFingerprint(c.Certificate[0]))
	}
	fmt.Printf("relay pass key %s\n   relays: relay -portal-key %s -allow-peer <coordinator id>\n", passPub, passPub)
	fmt.Printf("portal listening on %s://%s (registration open: %v, sign-up credit: %d)\n   web: %s://<host>%s/  admin: %s://<host>%s/admin/\n",
		mode, *listen, *open, *signup, mode, portPart(*listen), mode, portPart(*listen))
	if mode == "https" {
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
	case "useradd":
		fs := flag.NewFlagSet("useradd", flag.ExitOnError)
		isAdmin := fs.Bool("admin", false, "grant admin rights")
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: portal useradd [-admin] <username>")
		}
		pw, err := cli.ReadPassword("Password for "+fs.Arg(0)+": ", true)
		if err != nil {
			return err
		}
		u, err := p.CreateUser(fs.Arg(0), pw, *isAdmin)
		if err != nil {
			return err
		}
		return printJSON(u)
	case "passwd":
		if err := need(1, "<username>"); err != nil {
			return err
		}
		id, err := user(args[0])
		if err != nil {
			return err
		}
		pw, err := cli.ReadPassword("New password for "+args[0]+": ", true)
		if err != nil {
			return err
		}
		return st.SetPassword(id, pw)
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
		return st.SetDisabled(id, cmd == "disable")
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
