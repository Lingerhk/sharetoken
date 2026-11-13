package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildTypesShareNothing(t *testing.T) {
	dev, prod := profiles[EnvDev], profiles[EnvProd]
	if dev.Portal == prod.Portal || dev.HomeDir == prod.HomeDir || dev.HTTP == prod.HTTP {
		t.Fatalf("%+v %+v", dev, prod)
	}
	if Env != EnvDev {
		t.Fatalf("a build that does not say must be dev, not %q", Env)
	}
}

func TestHomeFollowsBuildType(t *testing.T) {
	defer func(e string) { Env = e }(Env)
	t.Setenv("SHARETOKEN_HOME", "")
	if h := Home(); filepath.Base(h) != ".sharetoken-dev" {
		t.Fatalf("dev home %q", h)
	}
	Env = EnvProd
	if h := Home(); filepath.Base(h) != ".sharetoken" {
		t.Fatalf("prod home %q", h)
	}
}

// A prod build cannot be sent to another portal by its environment.
func TestProdIgnoresPortalOverride(t *testing.T) {
	defer func(e string) { Env = e }(Env)
	t.Setenv("SHARETOKEN_PORTAL", "https://elsewhere.example")
	if PortalAddr() != "https://elsewhere.example" {
		t.Fatalf("dev: %q", PortalAddr())
	}
	Env = EnvProd
	if PortalAddr() != "https://sharetoken.app" {
		t.Fatalf("prod: %q", PortalAddr())
	}
}

func writeState(t *testing.T, home, config string) {
	t.Helper()
	for name, data := range map[string]string{ConfigFile: config, KeyFile: "key", NetworkFile: "{}"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(home, name string) bool {
	_, err := os.Stat(filepath.Join(home, name))
	return err == nil
}

// A config from before the build types is deleted with the device identity, and
// the client is logged out.
func TestOldConfigIsDeleted(t *testing.T) {
	home := t.TempDir()
	writeState(t, home, `{"portal":"https://sharetoken.app","token":"secret","user":"alice","http":"127.0.0.1:1234"}`)
	c, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "" || c.Portal != "" || c.User != "" || c.HTTP != DefaultHTTP() {
		t.Fatalf("old settings survived: %+v", c)
	}
	for _, f := range []string{ConfigFile, KeyFile, NetworkFile} {
		if exists(home, f) {
			t.Fatalf("%s survived", f)
		}
	}
}

// Another build type's directory is refused and left exactly as it is.
func TestOtherBuildTypesConfigIsRefused(t *testing.T) {
	home := t.TempDir()
	writeState(t, home, `{"version":1,"env":"prod","portal":"https://sharetoken.app","token":"secret"}`)
	_, err := LoadConfig(home)
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("want a refusal, got %v", err)
	}
	for _, f := range []string{ConfigFile, KeyFile, NetworkFile} {
		if !exists(home, f) {
			t.Fatalf("%s was deleted", f)
		}
	}
}

func TestSavedConfigCarriesBuildTypeAndLoadsBack(t *testing.T) {
	home := t.TempDir()
	c := DefaultConfig()
	c.Portal, c.Token, c.Env = "https://x", "t", ""
	if err := SaveConfig(home, c); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ConfigFile))
	var f struct {
		Env string `json:"env"`
	}
	if json.Unmarshal(data, &f) != nil || f.Env != Env {
		t.Fatalf("saved: %s", data)
	}
	got, err := LoadConfig(home)
	if err != nil || got.Token != "t" {
		t.Fatalf("%+v %v", got, err)
	}
}
