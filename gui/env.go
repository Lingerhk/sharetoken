package main

import (
	"fmt"
	"runtime"
	"strings"
)

// buildType is set at build time (scripts/build-gui.sh <dev|prod>), with -ldflags
// "-X main.buildType=sharetoken-build-type=prod". A build that does not say is
// dev. The prefix lets the build script find the value in a finished binary.
var buildType = buildTypePrefix + "dev"

const buildTypePrefix = "sharetoken-build-type="

// Env is what this shell was built for: envDev or envProd.
var Env = strings.TrimPrefix(buildType, buildTypePrefix)

const (
	envDev  = "dev"
	envProd = "prod"
)

// profile repeats client/env.go, which this module cannot import. firstSpare
// and lastSpare are the addresses tried when the default one is taken.
type profile struct {
	homeDir               string
	defaultHTTP           string
	firstSpare, lastSpare int
}

var profiles = map[string]profile{
	envProd: {homeDir: ".sharetoken", defaultHTTP: "127.0.0.1:9787", firstSpare: 9788, lastSpare: 9797},
	envDev:  {homeDir: ".sharetoken-dev", defaultHTTP: "127.0.0.1:9887", firstSpare: 9888, lastSpare: 9897},
}

func init() {
	if _, ok := profiles[Env]; !ok {
		panic(fmt.Sprintf("this app was built for an unknown environment %q", Env))
	}
}

func current() profile { return profiles[Env] }

// daemonName is the file name of the daemon of this build type.
func daemonName() string {
	name := "sharetoken-" + Env
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// windowTitle tells a dev window from the real thing.
func windowTitle() string {
	if Env == envProd {
		return "sharetoken"
	}
	return "sharetoken DEV"
}
