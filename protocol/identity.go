package protocol

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	lcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PubKeyFromPeerID extracts the Ed25519 public key inlined in a libp2p peer
// ID. Every sharetoken identity is an Ed25519 libp2p key, so a peer ID alone is
// enough to verify that party's signatures: no separate PKI is needed, and the
// key you verify with is the same one libp2p authenticated the connection with.
func PubKeyFromPeerID(id string) (ed25519.PublicKey, error) {
	pid, err := peer.Decode(id)
	if err != nil {
		return nil, fmt.Errorf("protocol: bad peer id: %w", err)
	}
	pk, err := pid.ExtractPublicKey()
	if err != nil {
		return nil, fmt.Errorf("protocol: peer id has no inline key: %w", err)
	}
	if pk.Type() != lcrypto.Ed25519 {
		return nil, errors.New("protocol: peer key is not ed25519")
	}
	raw, err := pk.Raw()
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("protocol: bad ed25519 key size")
	}
	return ed25519.PublicKey(raw), nil
}

func verifySig(signer string, msg, sig []byte) error {
	pub, err := PubKeyFromPeerID(signer)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return errors.New("protocol: bad signature")
	}
	return nil
}
