//go:build unix

package p2p

import "golang.org/x/sys/unix"

// numFDs is the process's descriptor limit, as libp2p's AutoScale reads it.
func numFDs() int {
	var l unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &l); err != nil {
		return 0
	}
	return int(l.Cur)
}
