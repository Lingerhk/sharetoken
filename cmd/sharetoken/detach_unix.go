//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// startDetached starts a program that outlives this one.
func startDetached(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
