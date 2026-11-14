package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// buildType is set at build time (scripts/build/build-gui.sh <dev|prod>), with -ldflags
// "-X sharetoken/internal/client.buildType=sharetoken-build-type=prod". A build that does
// not say is dev, so that forgetting cannot reach production. The prefix lets the
// build script find the value in a finished binary, also one for another system.
var buildType = buildTypePrefix + "dev"

const buildTypePrefix = "sharetoken-build-type="

// Env is what this client was built for: EnvDev or EnvProd.
var Env = strings.TrimPrefix(buildType, buildTypePrefix)

// Build types.
const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// profile is everything that differs between the build types. The address of
// the portal is the only network setting: it hands out the coordinator and the
// relays. gui/env.go repeats HomeDir and HTTP (the shell is a separate module).
type profile struct {
	Portal  string
	HomeDir string // under the user's home directory
	HTTP    string // local API and OpenAI endpoint
}

var profiles = map[string]profile{
	EnvProd: {Portal: "https://sharetoken.app", HomeDir: ".sharetoken", HTTP: "127.0.0.1:9787"},
	EnvDev:  {Portal: "https://dev.sharetoken.app", HomeDir: ".sharetoken-dev", HTTP: "127.0.0.1:9887"},
}

func init() {
	if _, ok := profiles[Env]; !ok {
		panic(fmt.Sprintf("this client was built for an unknown environment %q", Env))
	}
}

func current() profile { return profiles[Env] }

// BuiltInPortal is the portal this build signs in to.
func BuiltInPortal() string { return current().Portal }

// DefaultHTTP is the default address of the local API.
func DefaultHTTP() string { return current().HTTP }

// Home is the client's state directory: $SHARETOKEN_HOME, else the build type's
// directory under the user's home.
func Home() string {
	if h := os.Getenv("SHARETOKEN_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, current().HomeDir)
	}
	return current().HomeDir
}
