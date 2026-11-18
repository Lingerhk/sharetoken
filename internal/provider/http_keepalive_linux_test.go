//go:build linux

package provider

import (
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

func checkUserTimeout(t *testing.T, c net.Conn) {
	t.Helper()
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	_ = raw.Control(func(fd uintptr) {
		got, err = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
	})
	if err != nil || got != int(tcpUserTimeout.Milliseconds()) {
		t.Fatalf("TCP_USER_TIMEOUT = %d, %v", got, err)
	}
}
