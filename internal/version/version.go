// Package version says which build this is. Every program of a release carries
// the same version, from the VERSION file through scripts/ldflags.sh. It says
// which code runs; whether two programs can talk is decided by the interface
// versions (docs/versioning.md).
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Build information, set with -ldflags "-X sharetoken/internal/version.Name=value".
var (
	Version   = "dev" // 1.2.3, or 1.2.3-dev.<commit> outside a release; "dev" when not stamped
	Commit    = ""
	BuildTime = ""
)

// API is the version of the portal's and the local client's HTTP APIs: /api/v<API>/.
const API = 1

// Info describes this build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
}

// Current returns this build's information.
func Current() Info { return Info{Version: Version, Commit: Commit, BuildTime: BuildTime} }

// UserAgent names a program of this build: sharetoken-relay/1.2.3.
func UserAgent(program string) string { return "sharetoken-" + program + "/" + Version }

// String is what a program's version command and first log line print.
func String(program string) string {
	s := "sharetoken-" + program + " " + Version
	if Commit != "" {
		s += " (" + Commit + ")"
	}
	if BuildTime != "" {
		s += " built " + BuildTime
	}
	return s
}

// parse splits v into its numbers and pre-release part.
func parse(v string) (nums [3]int, pre string, ok bool) {
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// Valid reports whether v is a version: major.minor.patch with an optional
// -pre-release part.
func Valid(v string) bool {
	_, _, ok := parse(v)
	return ok
}

// IsDev reports whether v is not a release: not stamped, or a pre-release.
func IsDev(v string) bool {
	_, pre, ok := parse(v)
	return !ok || pre != ""
}

// Core returns v without its pre-release part. A development build of 1.2.3 has
// the protocols of 1.2.3: a network that serves development builds compares this.
func Core(v string) string {
	core, _, _ := strings.Cut(v, "-")
	return core
}

// Compare orders two valid versions: -1, 0 or 1. A pre-release comes before
// its release; two pre-releases of one version compare equal, since their
// commits have no order. It panics on an invalid version.
func Compare(a, b string) int {
	an, ap, aok := parse(a)
	bn, bp, bok := parse(b)
	if !aok || !bok {
		panic(fmt.Sprintf("version: comparing invalid versions %q and %q", a, b))
	}
	for i := range an {
		switch {
		case an[i] < bn[i]:
			return -1
		case an[i] > bn[i]:
			return 1
		}
	}
	switch {
	case ap != "" && bp == "":
		return -1
	case ap == "" && bp != "":
		return 1
	}
	return 0
}
