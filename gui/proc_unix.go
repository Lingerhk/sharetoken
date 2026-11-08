//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// daemonProcAttr puts the daemon in its own process group: a terminal's Ctrl-C
// reaches this process, which stops the daemon in order.
func daemonProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
