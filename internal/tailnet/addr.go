package tailnet

import (
	"errors"
	"fmt"
	"strings"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/tailscale/tailcat"
)

// P_TAILCAT is the multiaddr protocol of a tailcat server: /tailcat/<address>.
// The code is in the range multicodec leaves for private use. A program
// without this package does not know the name: it fails to parse such an
// address and, as everywhere in this code base, skips it.
const P_TAILCAT = 0x30746c

// ProtocolName is the multiaddr component's name.
const ProtocolName = "tailcat"

// maxAddrLen bounds a tailcat address in a multiaddr. An address that names
// its relay region in full is some 250 characters; nothing honest is longer
// than a few times that.
const maxAddrLen = 1024

func init() {
	if err := ma.AddProtocol(ma.Protocol{
		Name:       ProtocolName,
		Code:       P_TAILCAT,
		VCode:      ma.CodeToVarint(P_TAILCAT),
		Size:       ma.LengthPrefixedVarSize,
		Transcoder: ma.NewTranscoderFromFunctions(addrToBytes, addrToString, validAddrBytes),
	}); err != nil {
		panic(err)
	}
}

func addrToBytes(s string) ([]byte, error) {
	if err := validAddr(s); err != nil {
		return nil, err
	}
	return []byte(s), nil
}

func addrToString(b []byte) (string, error) {
	if err := validAddr(string(b)); err != nil {
		return "", err
	}
	return string(b), nil
}

func validAddrBytes(b []byte) error { return validAddr(string(b)) }

// validAddr checks the form only (what may stand in a multiaddr); whether
// the address decodes is ParseAddr's business, when it is dialed.
func validAddr(s string) error {
	if len(s) < 3 || len(s) > maxAddrLen || !strings.HasPrefix(s, "tc") {
		return errors.New("not a tailcat address")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("tailcat address: bad character %q", c)
		}
	}
	return nil
}

// Multiaddr is the multiaddr of a tailcat server.
func Multiaddr(a tailcat.Addr) (ma.Multiaddr, error) {
	return ma.NewMultiaddr("/" + ProtocolName + "/" + string(a))
}

// FromMultiaddr returns the tailcat address a multiaddr names, if it is one
// of this transport's.
func FromMultiaddr(m ma.Multiaddr) (tailcat.Addr, bool) {
	if m == nil {
		return "", false
	}
	first, rest := ma.SplitFirst(m)
	if first == nil || first.Protocol().Code != P_TAILCAT || len(rest) != 0 {
		return "", false
	}
	return tailcat.Addr(first.Value()), true
}

// Is reports whether m is a tailnet address.
func Is(m ma.Multiaddr) bool {
	_, ok := FromMultiaddr(m)
	return ok
}

// Addrs returns the tailnet addresses among addrs.
func Addrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	var out []ma.Multiaddr
	for _, a := range addrs {
		if Is(a) {
			out = append(out, a)
		}
	}
	return out
}
