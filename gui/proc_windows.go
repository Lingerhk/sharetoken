package main

import "syscall"

// createNoWindow is CREATE_NO_WINDOW: the daemon is a console program, and
// without this flag starting it from the app opens a console window. The flag
// also gives it its own console, so a terminal's Ctrl-C does not reach it.
const createNoWindow = 0x08000000

func daemonProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func processAlive(pid int) bool {
	const queryLimitedInformation, stillActive = 0x1000, 259
	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	return syscall.GetExitCodeProcess(h, &code) == nil && code == stillActive
}
