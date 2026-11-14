package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func platformCanUpdate() (bool, string) {
	exe, err := os.Executable()
	if err != nil {
		return false, err.Error()
	}
	if filepath.Dir(exe) != "/usr/bin" || !dpkgOwns(exe) {
		return false, "this copy was not installed from the .deb package"
	}
	if _, err := exec.LookPath("pkexec"); err != nil {
		return false, "pkexec is not installed"
	}
	return true, ""
}

// dpkgOwns says whether the package of this build type installed path.
func dpkgOwns(path string) bool {
	data, err := os.ReadFile(filepath.Join("/var/lib/dpkg/info", daemonName()+".list"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l == path {
			return true
		}
	}
	return false
}

// platformInstall installs the .deb with dpkg as root, asking through pkexec
// (the desktop's password dialog), then starts the app again once this one has
// left: the new copy takes the single-instance lock.
func platformInstall(ctx context.Context, u *shellUpdater, o *offer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "pkexec", "dpkg", "-i", o.Download.Path).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			return errors.New("installing needs an administrator's password, and none was given")
		}
		return fmt.Errorf("dpkg: %v: %s", err, lastLines(string(out), 3))
	}
	u.set(updateStatus{State: "restarting"})
	u.sup.stop()
	cmd := exec.Command("/bin/sh", "-c", `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$2"`, "sh", strconv.Itoa(os.Getpid()), exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = afterUpdateEnv()
	if err := cmd.Start(); err != nil {
		return errors.New("installed; start the app again: " + err.Error())
	}
	_ = cmd.Process.Release()
	u.quit()
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}

// cleanStaging: nothing to clean on Linux.
func cleanStaging() {}
