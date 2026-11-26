//go:build !windows

package diag

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func osVersion() string {
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
		if err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
	case "linux":
		data, err := os.ReadFile("/etc/os-release")
		if err == nil {
			id, ver := "", ""
			for _, l := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(l, "ID="); ok {
					id = strings.Trim(v, `"`)
				}
				if v, ok := strings.CutPrefix(l, "VERSION_ID="); ok {
					ver = strings.Trim(v, `"`)
				}
			}
			return strings.TrimSpace(id + " " + ver)
		}
	}
	return ""
}
