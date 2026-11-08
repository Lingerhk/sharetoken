package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigPath(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-config", "a.yaml", "serve"}, "a.yaml"},
		{[]string{"--config=b.yaml"}, "b.yaml"},
		{[]string{"-db", "x", "-config=c.yaml", "check"}, "c.yaml"},
		{[]string{"serve", "-listen", ":1"}, ""},
		{[]string{"--", "-config", "d"}, ""},
	} {
		if got := ConfigPath(c.args); got != c.want {
			t.Errorf("%v: %q, want %q", c.args, got, c.want)
		}
	}
}

type sample struct {
	Version int      `yaml:"version"`
	Name    string   `yaml:"name"`
	Wait    Duration `yaml:"wait"`
	Buf     Size     `yaml:"buf"`
	List    []string `yaml:"list"`
	Keep    string   `yaml:"keep"`
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAndFlagPrecedence(t *testing.T) {
	p := write(t, "version: 1\nname: file\nwait: 10m\nbuf: 16KiB\nlist: [a, b]\n")
	s := sample{Name: "default", Keep: "default", Wait: D("1s")}
	if err := Load(p, &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "file" || s.Wait.T() != 10*time.Minute || s.Buf != 16<<10 || s.Keep != "default" || len(s.List) != 2 {
		t.Fatalf("loaded %+v", s)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fl := Flags{FlagSet: fs}
	fs.StringVar(&s.Name, "name", s.Name, "")
	fs.StringVar(&s.Keep, "keep", s.Keep, "")
	fl.Duration(&s.Wait, "wait", "")
	fl.List(&s.List, "list", "")
	if err := fs.Parse([]string{"-name", "flag", "-list", "x, y,z"}); err != nil {
		t.Fatal(err)
	}
	// Flags not given leave the file's values alone.
	if s.Name != "flag" || s.Wait.T() != 10*time.Minute || strings.Join(s.List, ",") != "x,y,z" || s.Keep != "default" {
		t.Fatalf("after flags %+v", s)
	}
}

func TestRepeatedReplacesFileList(t *testing.T) {
	list := []string{"from-file"}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	Flags{FlagSet: fs}.Repeated(&list, "k", "")
	if err := fs.Parse([]string{"-k", "a", "-k", "b"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(list, ",") != "a,b" {
		t.Fatal(list)
	}
}

func TestLoadRejects(t *testing.T) {
	var s sample
	for body, want := range map[string]string{
		"version: 1\nnmae: typo\n":    "nmae",
		"version: 2\nname: x\n":       "version 2",
		"name: x\n":                   "version 0",
		"version: 1\nwait: 10 mins\n": "bad duration",
		"version: 1\nbuf: lots\n":     "bad size",
	} {
		err := Load(write(t, body), &s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

func TestResolveAndSecret(t *testing.T) {
	dir := t.TempDir()
	a, b, c, d := "k.key", "/abs/k", "", "mysql://u@h/db"
	Resolve(dir, &a, &b, &c, &d)
	if a != filepath.Join(dir, "k.key") || b != "/abs/k" || c != "" || d != "mysql://u@h/db" {
		t.Fatal(a, b, c, d)
	}
	f := filepath.Join(dir, "pw")
	_ = os.WriteFile(f, []byte("s3cret\n"), 0o644)
	var warned string
	got, err := Secret("ST_TEST_UNSET_VAR", f, func(m string) { warned = m })
	if err != nil || got != "s3cret" || !strings.Contains(warned, "readable") {
		t.Fatal(got, err, warned)
	}
	t.Setenv("ST_TEST_PW", "fromenv")
	if got, _ := Secret("ST_TEST_PW", f, nil); got != "fromenv" {
		t.Fatal(got)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]Size{"16384": 16384, "16KiB": 16 << 10, "4 MiB": 4 << 20, "1GiB": 1 << 30, "2KB": 2000} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
}
