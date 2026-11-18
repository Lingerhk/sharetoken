//go:build !linux

package provider

import (
	"net"
	"testing"
)

func checkUserTimeout(*testing.T, net.Conn) {}
