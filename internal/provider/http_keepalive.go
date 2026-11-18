package provider

import (
	"net"
	"time"
)

// A connection to a model server that died without a word (a NAT that
// dropped it, a server that went away) is found by TCP keep-alive. Go's
// default probes after 15 s idle and gives up only after nine missed probes
// at 15 s each, some 2.5 minutes, while a stream waits on it; these probes
// find it in about 30 s (sub2api 5854a1db4). On Linux TCP_USER_TIMEOUT also
// bounds how long sent data may go unacknowledged (keepAliveControl).
var providerKeepAlive = net.KeepAliveConfig{
	Enable:   true,
	Idle:     15 * time.Second,
	Interval: 5 * time.Second,
	Count:    3,
}

// tcpUserTimeout is how long data written to a model server may go
// unacknowledged before the connection is given up (Linux only).
const tcpUserTimeout = 30 * time.Second

// providerDialer dials model servers with the keep-alive above.
func providerDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:         30 * time.Second,
		KeepAliveConfig: providerKeepAlive,
		Control:         keepAliveControl,
	}
}
