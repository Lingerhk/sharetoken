//go:build !linux

package provider

import "syscall"

// keepAliveControl does nothing where TCP_USER_TIMEOUT does not exist: the
// keep-alive probes alone find a dead connection.
func keepAliveControl(string, string, syscall.RawConn) error { return nil }
