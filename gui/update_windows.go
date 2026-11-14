package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createBreakawayFromJob = 0x01000000

func platformCanUpdate() (bool, string) {
	exe, err := os.Executable()
	if err != nil {
		return false, err.Error()
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "uninstall.exe")); err != nil {
		return false, "this copy was not installed with the installer"
	}
	return true, ""
}

// platformInstall runs the installer silently. It stops the app, replaces the
// programs and, told /RELAUNCH, starts the app again. cmd's start runs it, so it
// is no child of this process: the installer ends this process's tree.
func platformInstall(_ context.Context, u *shellUpdater, o *offer) error {
	if o.Download == nil || o.Download.Path == "" {
		return errors.New("no package")
	}
	u.set(updateStatus{State: "restarting"})
	u.sup.stop()
	run := func(flags uint32) error {
		cmd := exec.Command("cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags,
			CmdLine: `cmd.exe /d /c start "" "` + o.Download.Path + `" /S /RELAUNCH`}
		cmd.Env = afterUpdateEnv()
		return cmd.Run()
	}
	// Out of this process's job, if it has one that allows it, which ends
	// everything in it with this process.
	if err := run(createNoWindow | createBreakawayFromJob); err != nil {
		if err := run(createNoWindow); err != nil {
			return errors.New("starting the installer: " + err.Error() + "; restart the app")
		}
	}
	u.quit()
	return nil
}

// cleanStaging: nothing to clean on Windows.
func cleanStaging() {}
