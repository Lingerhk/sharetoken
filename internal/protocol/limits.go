package protocol

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	ma "github.com/multiformats/go-multiaddr"
)

// Limits of an offer. An offer is written by its node and kept, whole, in the
// coordinator's memory; a ticket copies its addresses to the buyer, and the
// model list is built from every offer's models. Without bounds one account
// could fill the coordinator's memory, or make a ticket or the model list
// larger than a frame. Every limit is far above what a client sends: a node
// lists the addresses of its interfaces, of its relays and of its tunnel
// (some tens at most), and the models its user configured.
const (
	MaxOfferModels  = 128
	MaxModelIDBytes = 128
	MaxOfferAddrs   = 64
	MaxAddrBytes    = 256
	// A tunnel address that names its relay region in full is some 250
	// characters, and the tailnet package takes up to 1024.
	MaxTunnelAddrBytes = 1024 + len(tunnelAddrPrefix)
	// MaxOfferConcurrency bounds the slots a node claims (0: no limit of its own).
	MaxOfferConcurrency = 4096
	// MaxPriceMicros bounds a price per million tokens: 100,000 credits, a
	// tenth of a credit per token. No model costs a thousandth of it.
	MaxPriceMicros = 100_000_000_000
	// MaxQuoteBytes bounds a TEE quote; a TDX quote with its certificates is
	// under 16 KiB.
	MaxQuoteBytes = 64 << 10
	// MaxOfferFrame bounds an announce frame: an offer at every limit above,
	// as JSON, is under 200 KiB.
	MaxOfferFrame = 256 << 10

	maxOfferVersionBytes  = 64
	maxOfferInferVersions = 16
	maxModelAPIs          = 8
)

// tunnelAddrPrefix begins the multiaddr of a tailnet tunnel (package
// tailnet, which this package cannot import; a program without it cannot
// even parse the address, so it is checked as text).
const tunnelAddrPrefix = "/tailcat/"

// checkLimits refuses an offer larger than any node's.
func (o *Offer) checkLimits() error {
	if len(o.Models) > MaxOfferModels {
		return fmt.Errorf("offer: %d models, at most %d", len(o.Models), MaxOfferModels)
	}
	for _, m := range o.Models {
		if err := CheckModelID(m.Model); err != nil {
			return fmt.Errorf("offer: %w", err)
		}
		if max(m.PriceInMicros, m.PriceOutMicros, m.PriceCacheRead, m.PriceCacheWrite) > MaxPriceMicros {
			return fmt.Errorf("offer: a price of model %q is above %d micros per million tokens", m.Model, uint64(MaxPriceMicros))
		}
		if !m.Prices().Fair() {
			return fmt.Errorf("offer: the cache prices of model %q are out of proportion: a cache read may cost the input price at most, a cache write %d times it", m.Model, MaxCacheWriteFactor)
		}
		if len(m.APIs) > maxModelAPIs {
			return errors.New("offer: too many APIs for a model")
		}
	}
	if len(o.Addrs) > MaxOfferAddrs {
		return fmt.Errorf("offer: %d addresses, at most %d", len(o.Addrs), MaxOfferAddrs)
	}
	for _, a := range o.Addrs {
		if err := CheckNodeAddr(a); err != nil {
			return fmt.Errorf("offer: %w", err)
		}
	}
	if o.MaxConcurrency > MaxOfferConcurrency {
		return fmt.Errorf("offer: concurrency %d, at most %d", o.MaxConcurrency, MaxOfferConcurrency)
	}
	if len(o.ClientVersion) > maxOfferVersionBytes || len(o.InferVersions) > maxOfferInferVersions {
		return errors.New("offer: client or protocol version too long")
	}
	if q := o.Quote; q != nil && len(q.Backend)+len(q.Evidence) > MaxQuoteBytes {
		return errors.New("offer: attestation quote too large")
	}
	return nil
}

// CheckModelID refuses what cannot be a marketplace model ID: an empty or
// very long one, or one with characters that do not print (control and
// format characters would garble a model list or a log line). Sellers name
// their models themselves, so letters of any script, digits, punctuation and
// inner spaces all pass: gpt-5.5, qwen2.5:7b, meta-llama/Llama-3-70B.
func CheckModelID(id string) error {
	switch {
	case id == "":
		return errors.New("empty model id")
	case len(id) > MaxModelIDBytes:
		return fmt.Errorf("model id of %d bytes, at most %d", len(id), MaxModelIDBytes)
	case !utf8.ValidString(id), id != strings.TrimSpace(id):
		return fmt.Errorf("model id %q is not printable text", id)
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("model id %q is not printable text", id)
		}
	}
	return nil
}

// CheckNodeAddr refuses an address a node cannot be reached at with the
// transports this network uses: TCP, QUIC, either of them through a circuit
// relay, or a tailnet tunnel. libp2p knows more (WebSocket, WebTransport,
// WebRTC); a host here runs none of them, and a buyer must not be sent to
// dial what a seller made up.
func CheckNodeAddr(addr string) error {
	if strings.HasPrefix(addr, tunnelAddrPrefix) {
		rest := addr[len(tunnelAddrPrefix):]
		if len(addr) > MaxTunnelAddrBytes || rest == "" || strings.ContainsFunc(rest, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) {
			return errors.New("malformed tunnel address")
		}
		return nil
	}
	if len(addr) > MaxAddrBytes {
		return fmt.Errorf("address of %d bytes, at most %d", len(addr), MaxAddrBytes)
	}
	m, err := ma.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("address %q: %v", addr, err)
	}
	if !DialableAddr(m) {
		return fmt.Errorf("address %q is not TCP or QUIC, directly or through a relay", addr)
	}
	return nil
}

// DialableAddr reports whether m is an address of the transports this
// network uses, tunnels aside:
//
//	/<ip4|ip6|dns|dns4|dns6>/<host>/tcp/<port>
//	/<ip4|ip6|dns|dns4|dns6>/<host>/udp/<port>/quic-v1
//
// either alone, or followed by /p2p/<relay>/p2p-circuit, and in both cases
// by an optional /p2p/<peer>.
func DialableAddr(m ma.Multiaddr) bool {
	codes := make([]int, 0, len(m))
	for _, c := range m {
		codes = append(codes, c.Protocol().Code)
	}
	next := func(want ...int) bool {
		if len(codes) < len(want) {
			return false
		}
		for i, w := range want {
			if codes[i] != w {
				return false
			}
		}
		codes = codes[len(want):]
		return true
	}
	if len(codes) == 0 {
		return false
	}
	switch codes[0] {
	case ma.P_IP4, ma.P_IP6, ma.P_DNS, ma.P_DNS4, ma.P_DNS6:
		codes = codes[1:]
	default:
		return false
	}
	if !next(ma.P_TCP) && !next(ma.P_UDP, ma.P_QUIC_V1) {
		return false
	}
	_ = next(ma.P_P2P, ma.P_CIRCUIT)
	_ = next(ma.P_P2P)
	return len(codes) == 0
}
