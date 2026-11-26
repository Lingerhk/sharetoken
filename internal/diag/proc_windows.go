package diag

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
)

func pidAlive(pid int) bool {
	const queryLimitedInformation, stillActive = 0x1000, 259
	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	return syscall.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

func osVersion() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("windows %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}
