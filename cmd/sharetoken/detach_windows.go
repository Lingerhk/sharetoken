package main

import (
	"os/exec"
	"strings"
	"syscall"
)

const (
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
)

// startDetached starts a program that outlives this one and is no child of it:
// cmd's start runs it and cmd leaves, so a taskkill /T of this program's tree
// (the installer's own) does not reach it. It also leaves this program's job,
// if the job allows: an SSH session's job ends everything in it when the
// session closes. cmd does not take Go's quoting, so the command line is
// written out.
func startDetached(path string, args ...string) error {
	line := `cmd.exe /d /c start "" "` + path + `"`
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	run := func(flags uint32) error {
		cmd := exec.Command("cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags, CmdLine: line}
		return cmd.Run()
	}
	if err := run(createNoWindow | createBreakawayFromJob); err == nil {
		return nil
	}
	return run(createNoWindow) // the job does not let it go
}
