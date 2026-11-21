package p2p

import (
	"fmt"
	"net"
	"syscall"
	"testing"

	"golang.org/x/net/ipv4"
)

// Errors about one destination are skipped; others still end the reads. The
// socket keeps what quic-go asks of an OOB-capable one.
func TestTolerantUDPConn(t *testing.T) {
	for _, err := range []error{syscall.EADDRNOTAVAIL, syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH,
		&net.OpError{Op: "read", Net: "udp", Err: fmt.Errorf("recvmsg: %w", syscall.EADDRNOTAVAIL)}} {
		if !unreachable(err) {
			t.Errorf("%v is not skipped", err)
		}
	}
	if unreachable(net.ErrClosed) || unreachable(syscall.EBADF) {
		t.Error("a closed socket is skipped")
	}
	pc, err := listenTolerantUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	type oobCapable interface {
		net.PacketConn
		SyscallConn() (syscall.RawConn, error)
		SetReadBuffer(int) error
		ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error)
		WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error)
	}
	if _, ok := pc.(oobCapable); !ok {
		t.Fatal("the socket lost its OOB methods")
	}
	if _, ok := pc.(interface {
		ReadBatch(ms []ipv4.Message, flags int) (int, error)
	}); !ok {
		t.Fatal("quic-go would read the descriptor itself, past the skipping")
	}
	// a packet still arrives
	out, _ := net.DialUDP("udp4", nil, pc.LocalAddr().(*net.UDPAddr))
	defer out.Close()
	_, _ = out.Write([]byte("hi"))
	b := make([]byte, 16)
	n, _, _, _, err := pc.(oobCapable).ReadMsgUDP(b, make([]byte, 64))
	if err != nil || string(b[:n]) != "hi" {
		t.Fatalf("%q %v", b[:n], err)
	}
}
