// Package config loads the YAML configuration files of the server binaries
// (relay, coordinator, portal); see configs/*.example.yaml and
// docs/configuration.md.
//
// Precedence: built-in defaults < the YAML file < command-line flags that are
// given explicitly. A binary fills its settings struct with defaults, loads
// the file named by -config over it, resolves the file's relative paths
// against the file's directory, and then binds its flags to the struct's
// fields with the current values as flag defaults, so only flags actually
// passed override the file.
package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Version is the configuration format version this build reads.
const Version = 1

// ConfigPath finds -config / --config (with "=" or as the next argument) in
// args without parsing anything else. "" if it is absent.
func ConfigPath(args []string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		for _, p := range []string{"-config", "--config"} {
			if a == p && i+1 < len(args) {
				return args[i+1]
			}
			if v, ok := strings.CutPrefix(a, p+"="); ok {
				return v
			}
		}
	}
	return ""
}

// Load decodes the YAML file at path into v, rejecting unknown fields and a
// version other than Version. v must already hold the defaults.
func Load(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var head struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if head.Version != Version {
		return fmt.Errorf("%s: version %d, this build reads version %d", path, head.Version, Version)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Dir returns the directory relative paths in the file at path resolve
// against.
func Dir(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Dir(abs)
	}
	return filepath.Dir(path)
}

// Resolve makes each non-empty relative path absolute against dir.
func Resolve(dir string, paths ...*string) {
	for _, p := range paths {
		if *p != "" && !filepath.IsAbs(*p) && !strings.Contains(*p, "://") {
			*p = filepath.Join(dir, *p)
		}
	}
}

// Secret reads a secret from the environment variable env or the file file
// (trailing newline removed). Both empty: "". A file readable by others is
// reported through warn.
func Secret(env, file string, warn func(string)) (string, error) {
	if env != "" {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
	}
	if file == "" {
		return "", nil
	}
	if st, err := os.Stat(file); err == nil && st.Mode().Perm()&0o077 != 0 && warn != nil {
		warn(fmt.Sprintf("secret file %s is readable by other users (mode %v); chmod 600 it", file, st.Mode().Perm()))
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// Database is the database section shared by the coordinator and the portal.
type Database struct {
	DSN             string   `yaml:"dsn"`
	PasswordEnv     string   `yaml:"password_env"`
	PasswordFile    string   `yaml:"password_file"`
	MaxOpenConns    int      `yaml:"max_open_conns"`
	MaxIdleConns    int      `yaml:"max_idle_conns"`
	ConnMaxLifetime Duration `yaml:"conn_max_lifetime"`
	ConnectTimeout  Duration `yaml:"connect_timeout"`
	LockWaitTimeout Duration `yaml:"lock_wait_timeout"`
}

// DefaultDatabase is the database section's defaults.
func DefaultDatabase(dsn string) Database {
	return Database{DSN: dsn, PasswordEnv: "SHARETOKEN_DB_PASSWORD", MaxOpenConns: 32, MaxIdleConns: 8,
		ConnMaxLifetime: D("30m"), ConnectTimeout: D("10s"), LockWaitTimeout: D("10s")}
}

// Log is the log section.
type Log struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // text, json
	File   string `yaml:"file"`   // "": stderr
}

// DefaultLog is the log section's defaults.
func DefaultLog() Log { return Log{Level: "info", Format: "text"} }

// Setup installs the logger described by l as the default for both log and
// slog, and returns it. The returned closer closes the log file.
func (l Log) Setup() (*slog.Logger, func(), error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(l.Level)); err != nil {
		return nil, nil, fmt.Errorf("log.level %q: want debug, info, warn or error", l.Level)
	}
	var w io.Writer = os.Stderr
	closer := func() {}
	if l.File != "" {
		f, err := os.OpenFile(l.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return nil, nil, err
		}
		w, closer = f, func() { _ = f.Close() }
	}
	var h slog.Handler
	switch l.Format {
	case "", "text":
		if l.File == "" && l.Level == "info" {
			// The default: keep the plain "date time message" lines of package log.
			log.SetOutput(w)
			return slog.Default(), closer, nil
		}
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	case "json":
		h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	default:
		return nil, nil, fmt.Errorf("log.format %q: want text or json", l.Format)
	}
	lg := slog.New(h)
	slog.SetDefault(lg) // also routes package log through h
	return lg, closer, nil
}

// Size is a byte count written as 16384, "16KiB", "4MiB" or "1GiB".
type Size int

// ParseSize parses a Size.
func ParseSize(s string) (Size, error) {
	s = strings.TrimSpace(s)
	mult := 1
	for _, u := range []struct {
		suf string
		m   int
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"KB", 1000}, {"MB", 1000_000}, {"B", 1}} {
		if v, ok := strings.CutSuffix(s, u.suf); ok {
			s, mult = strings.TrimSpace(v), u.m
			break
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad size %q (want e.g. 16384 or 16KiB)", s)
	}
	return Size(n * mult), nil
}

func (z *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseSize(n.Value)
	*z = v
	return err
}
func (z Size) String() string { return strconv.Itoa(int(z)) }
func (z *Size) Set(s string) error {
	v, err := ParseSize(s)
	*z = v
	return err
}

// Flags binds flags to configuration fields. Defaults shown in -help are the
// values after the file was loaded.
type Flags struct{ *flag.FlagSet }

// List binds a comma-separated list flag: when given, it replaces the list.
func (f Flags) List(p *[]string, name, usage string) {
	f.Var((*listFlag)(p), name, usage)
}

// Repeated binds a repeatable flag: when given (once or more), its values
// replace the list.
func (f Flags) Repeated(p *[]string, name, usage string) {
	f.Var(&repeatedFlag{p: p}, name, usage)
}

// Size binds a size flag.
func (f Flags) Size(p *Size, name, usage string) { f.Var(p, name, usage) }

// Duration binds a duration flag.
func (f Flags) Duration(p *Duration, name, usage string) { f.Var(p, name, usage) }

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error {
	*l = nil
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

type repeatedFlag struct {
	p   *[]string
	set bool
}

func (r *repeatedFlag) String() string {
	if r.p == nil {
		return ""
	}
	return strings.Join(*r.p, ",")
}
func (r *repeatedFlag) Set(s string) error {
	if !r.set {
		*r.p, r.set = nil, true
	}
	*r.p = append(*r.p, s)
	return nil
}
