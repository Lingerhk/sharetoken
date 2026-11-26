//go:build !windows

package diag

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func systemCountry() string {
	if runtime.GOOS == "darwin" {
		// The region of System Settings, even when the language is another's.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleLocale").Output(); err == nil {
			if c := countryOfLocale(strings.TrimSpace(string(out))); c != "" {
				return c
			}
		}
	}
	return envCountry()
}

func systemZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if t, err := os.Readlink("/etc/localtime"); err == nil {
		if z := zoneOfLink(t); z != "" {
			return z
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}
