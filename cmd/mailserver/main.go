// Command mailserver is sharetoken's email server: it delivers what the portal
// puts in the outbox (verification codes, notices, replies) and receives mail
// for a few mailboxes such as support@. See docs/mail.md.
//
//	mailserver -config mail.yaml serve
//	mailserver -config mail.yaml check-config
//	mailserver -config mail.yaml dkim-keygen    # creates dkim.key_file; prints its TXT record
//	mailserver -config mail.yaml dns            # the DNS records to publish, and what is published now
//	mailserver -config mail.yaml migrate        # create the tables (needs an account that may; the portal also does this)
//	mailserver -config mail.yaml send-test <to> # queue a test message, delivered by the running server
//
// It shares the portal's database, and should use an account limited to the
// mail tables, since it is the one sharetoken process strangers talk to.
package main

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	netmail "net/mail"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"sharetoken/internal/config"
	"sharetoken/internal/mailer"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/version"
)

// File is mail.yaml (configs/mail.example.yaml).
type File struct {
	Version  int             `yaml:"version"`
	Database config.Database `yaml:"database"`
	// Hostname is the name this server greets with; MX points to it. Domain is
	// what it sends and receives mail for.
	Hostname string `yaml:"hostname"`
	Domain   string `yaml:"domain"`
	From     string `yaml:"from"` // sender of send-test
	DKIM     struct {
		Selector string `yaml:"selector"`
		KeyFile  string `yaml:"key_file"`
	} `yaml:"dkim"`
	Delivery struct {
		Mode      string `yaml:"mode"` // direct, smarthost or resend
		Smarthost struct {
			Addr         string `yaml:"addr"`
			Username     string `yaml:"username"`
			PasswordFile string `yaml:"password_file"`
		} `yaml:"smarthost"`
		Resend struct {
			APIKeyFile string `yaml:"api_key_file"`
		} `yaml:"resend"`
		Workers  int             `yaml:"workers"`
		RetryFor config.Duration `yaml:"retry_for"`
		KeepFor  config.Duration `yaml:"keep_for"`
	} `yaml:"delivery"`
	SMTP struct {
		Enabled bool   `yaml:"enabled"`
		Listen  string `yaml:"listen"`
		// Addresses accepted; everything else is refused during the SMTP dialogue.
		Mailboxes      []string    `yaml:"mailboxes"`
		MaxMessageSize config.Size `yaml:"max_message_size"`
		MaxInboxSize   config.Size `yaml:"max_inbox_size"`
		MaxConns       int         `yaml:"max_conns"`
		MaxConnsPerIP  int         `yaml:"max_conns_per_ip"`
		MaxPerIPHour   int         `yaml:"max_per_ip_hour"`
		TLS            struct {
			CertFile string `yaml:"cert_file"` // renewals are picked up; empty: self-signed
			KeyFile  string `yaml:"key_file"`
		} `yaml:"tls"`
	} `yaml:"smtp"`
	Log config.Log `yaml:"log"`
}

func defaults() File {
	var f File
	f.Version = config.Version
	f.Database = config.DefaultDatabase("")
	f.DKIM.KeyFile = "dkim.key"
	f.Delivery.Mode = "direct"
	f.Delivery.Workers = 4
	f.Delivery.RetryFor, f.Delivery.KeepFor = config.D("48h"), config.D("2160h")
	f.SMTP.Listen = ":25"
	f.Log = config.DefaultLog()
	return f
}

func (f *File) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if f.Database.DSN == "" {
		bad("database.dsn is empty")
	}
	if f.Hostname == "" || f.Domain == "" {
		bad("hostname and domain are required")
	}
	if f.DKIM.Selector == "" || f.DKIM.KeyFile == "" {
		bad("dkim.selector and dkim.key_file are required: receivers treat unsigned mail from this domain as forged (DMARC)")
	}
	switch f.Delivery.Mode {
	case "direct":
	case "smarthost":
		if f.Delivery.Smarthost.Addr == "" {
			bad("delivery.mode smarthost needs delivery.smarthost.addr")
		}
	case "resend":
		if f.Delivery.Resend.APIKeyFile == "" {
			bad("delivery.mode resend needs delivery.resend.api_key_file")
		}
	default:
		bad("delivery.mode %q: want direct, smarthost or resend", f.Delivery.Mode)
	}
	if f.From != "" {
		a, err := netmail.ParseAddress(f.From)
		switch {
		case err != nil:
			bad("from: %v", err)
		case !strings.HasSuffix(strings.ToLower(a.Address), "@"+strings.ToLower(f.Domain)):
			bad("from %s is not in domain %s", a.Address, f.Domain)
		}
	}
	if f.SMTP.Enabled {
		if len(f.SMTP.Mailboxes) == 0 {
			bad("smtp.mailboxes is empty: nothing could be received")
		}
		for _, m := range f.SMTP.Mailboxes {
			if !strings.Contains(m, "@") {
				bad("smtp.mailboxes: %q is not a full address", m)
			}
		}
		if (f.SMTP.TLS.CertFile == "") != (f.SMTP.TLS.KeyFile == "") {
			bad("smtp.tls.cert_file and key_file go together")
		}
	}
	return errors.Join(errs...)
}

func main() {
	f := defaults()
	path := config.ConfigPath(os.Args[1:])
	if path != "" {
		if err := config.Load(path, &f); err != nil {
			log.Fatal(err)
		}
		config.Resolve(config.Dir(path), &f.Database.DSN, &f.Database.PasswordFile, &f.DKIM.KeyFile, &f.SMTP.TLS.CertFile,
			&f.SMTP.TLS.KeyFile, &f.Delivery.Smarthost.PasswordFile, &f.Delivery.Resend.APIKeyFile, &f.Log.File)
	}
	flag.String("config", "", "YAML configuration file (configs/mail.example.yaml)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mailserver -config mail.yaml <serve|check-config|dkim-keygen|dns|migrate|send-test TO>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cmd, args := flag.Arg(0), flag.Args()[1:]
	if err := f.validate(); err != nil {
		log.Fatalf("configuration:\n%v", err)
	}
	switch cmd {
	case "version":
		fmt.Println(version.String("mailserver"))
		return
	case "check-config":
		_ = yaml.NewEncoder(os.Stdout).Encode(f)
		return
	case "dkim-keygen":
		if err := mailer.GenerateDKIMKey(f.DKIM.KeyFile); err != nil {
			log.Fatal(err)
		}
		printDKIM(&f)
		return
	case "dns":
		printDKIM(&f)
		key, err := mailer.LoadDKIMKey(f.DKIM.KeyFile)
		if err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, c := range mailer.CheckDNS(ctx, nil, mailer.DNSExpect{Domain: f.Domain, Hostname: f.Hostname, Selector: f.DKIM.Selector, Key: key}) {
			state := "ok "
			if !c.OK {
				state = "BAD"
			}
			fmt.Printf("%s %-46s %s\n", state, c.Name, c.Detail)
		}
		return
	}

	lg, closeLog, err := f.Log.Setup()
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()
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
	store := &mailer.Store{DB: db}

	switch cmd {
	case "migrate":
		if err := mailer.Migrate(db); err != nil {
			log.Fatal(err)
		}
		return
	case "send-test":
		if len(args) != 1 || f.From == "" {
			log.Fatal("usage: mailserver -config mail.yaml send-test <to> (needs from: in the file)")
		}
		id, err := store.Enqueue(mailer.Message{Kind: mailer.KindTest, From: f.From, To: args[0], Subject: "ShareToken 邮件服务测试",
			Text: "这是一封测试邮件，来自 " + f.Hostname + "。\r\n时间：" + time.Now().UTC().Format(time.RFC3339) + "\r\n"})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("queued as %d; see the admin console or the server's log\n", id)
		return
	case "serve":
		if err := mysqldb.CheckSchema(db, "mail", mysqldb.Latest(mailer.Migrations)); err != nil {
			log.Fatal(err)
		}
		log.Print(version.String("mailserver"))
	default:
		log.Fatalf("unknown command %q", cmd)
	}

	key, err := mailer.LoadDKIMKey(f.DKIM.KeyFile)
	if err != nil {
		log.Fatalf("dkim.key_file: %v (create one with: mailserver -config … dkim-keygen)", err)
	}
	var transport mailer.Transport = &mailer.Direct{EHLO: f.Hostname}
	if f.Delivery.Mode == "smarthost" {
		sh := &mailer.Smarthost{Addr: f.Delivery.Smarthost.Addr, Username: f.Delivery.Smarthost.Username, EHLO: f.Hostname}
		if f.Delivery.Smarthost.PasswordFile != "" {
			if sh.Password, err = mailer.ReadSecretFile(f.Delivery.Smarthost.PasswordFile); err != nil {
				log.Fatalf("delivery.smarthost.password_file: %v", err)
			}
		}
		transport = sh
	}
	var api mailer.APITransport
	name := transport.Name()
	if f.Delivery.Mode == "resend" {
		k, err := mailer.ReadSecretFile(f.Delivery.Resend.APIKeyFile)
		if err != nil || k == "" {
			log.Fatalf("delivery.resend.api_key_file: empty or unreadable (%v)", err)
		}
		if api, err = mailer.NewResend(k, ""); err != nil {
			log.Fatal(err)
		}
		name = api.Name()
	}
	worker := mailer.NewWorker(mailer.WorkerConfig{Store: store, Transport: transport, API: api,
		Signers: []*mailer.DKIMSigner{{Domain: f.Domain, Selector: f.DKIM.Selector, Key: crypto.Signer(key)}},
		Workers: f.Delivery.Workers, RetryFor: f.Delivery.RetryFor.T(), KeepFor: f.Delivery.KeepFor.T(), Log: lg})

	sc := mailer.ServerConfig{Store: store, Worker: worker, Hostname: f.Hostname, Domain: f.Domain, Selector: f.DKIM.Selector, Key: key, Delivery: name, Log: lg}
	if f.SMTP.Enabled {
		tlsCfg, err := serverTLS(&f)
		if err != nil {
			log.Fatal(err)
		}
		l, err := net.Listen("tcp", f.SMTP.Listen)
		if err != nil {
			log.Fatal(err)
		}
		sc.Listener = l
		sc.Inbound = mailer.NewInbound(mailer.InboundConfig{Store: store, Listen: f.SMTP.Listen, Hostname: f.Hostname, Mailboxes: f.SMTP.Mailboxes, TLS: tlsCfg,
			MaxMessageBytes: int64(f.SMTP.MaxMessageSize), MaxInboxBytes: int64(f.SMTP.MaxInboxSize), MaxConns: f.SMTP.MaxConns, MaxConnsPerIP: f.SMTP.MaxConnsPerIP,
			MaxPerIPHour: f.SMTP.MaxPerIPHour, Log: lg})
		fmt.Printf("receiving on %s for %s\n", f.SMTP.Listen, strings.Join(f.SMTP.Mailboxes, ", "))
	}
	fmt.Printf("delivering (%s) as %s\n", name, f.Hostname)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mailer.Run(ctx, sc); err != nil {
		log.Fatal(err)
	}
}

func serverTLS(f *File) (*tls.Config, error) {
	if f.SMTP.TLS.CertFile == "" {
		return mailer.SelfSignedTLS(f.Hostname)
	}
	cl := &mailer.CertLoader{Cert: f.SMTP.TLS.CertFile, Key: f.SMTP.TLS.KeyFile}
	if _, err := cl.Get(nil); err != nil {
		return nil, fmt.Errorf("smtp.tls: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: cl.Get}, nil
}

func printDKIM(f *File) {
	key, err := mailer.LoadDKIMKey(f.DKIM.KeyFile)
	if err != nil {
		log.Fatal(err)
	}
	rec, err := mailer.DKIMRecord(key)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("TXT %s._domainkey.%s\n%s\n", f.DKIM.Selector, f.Domain, rec)
}
