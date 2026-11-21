//go:build !windows

package warmup

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a process group of its own, which a timeout ends
// whole: the agent's own children with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
