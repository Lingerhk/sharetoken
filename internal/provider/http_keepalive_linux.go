//go:build linux

package provider

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// keepAliveControl sets TCP_USER_TIMEOUT on a TCP socket. A kernel or
// sandbox that refuses it leaves the connection as it was: the dial goes on
// (sub2api 94e56dd19).
func keepAliveControl(network, _ string, c syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil
	}
	_ = c.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(tcpUserTimeout.Milliseconds()))
	})
	return nil
}
