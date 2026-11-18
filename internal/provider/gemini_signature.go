package provider

import (
	lru "container/list"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
)

// Gemini 3 signs the first function call of each step it thinks through
// (thoughtSignature), and wants that signature back on the call when the
// conversation comes again; without it the tool loop loses the model's
// reasoning between steps. A chat client knows no such field, but every
// client sends a call's id back as it was given. So the signature rides in
// the id the buyer is given, and comes off it on the way back (after
// magpie's gemini_signature.go):
//
//	<id>__ts<n>__<signature, base64url>   a short signature, inline
//	<id>__tsk16__<key>                    a long one, kept by this seller
//
// n is how many characters follow the mark, so an id a client shortened
// is told from a whole one: a partial signature is none (Google answers
// one with 400 "Corrupted thought signature"). A signature is several KB
// once the model thought for a while, too long for an id some clients
// index, so long ones stay in a store of the seller's own and the id
// carries a 16-character key. A key this seller does not have (another
// seller served the earlier turn, or this one restarted) reads as no
// signature, and the call goes with the placeholder Google documents for
// history it did not sign.

const (
	sigMark = "__ts"
	sigRaw  = "r" // the signature's own text, when it is not canonical base64
	sigKept = "k" // a key into the seller's store
	sigEnd  = "__"
	// sigInlineMax is the longest encoded signature carried in the id
	// itself: past it the id carries a key.
	sigInlineMax = 64
	sigKeyBytes  = 12 // 16 base64url characters
)

// signedID is a tool call's id carrying its thought signature.
func signedID(id, sig string, store *sigStore) string {
	if sig == "" || sig == skipThoughtSignature {
		return id
	}
	if id == "" {
		id = "call_" + randomHex(12)
	}
	kind, enc := sigRaw, base64.RawURLEncoding.EncodeToString([]byte(sig))
	if b, err := base64.StdEncoding.DecodeString(sig); err == nil && base64.StdEncoding.EncodeToString(b) == sig {
		kind, enc = "", base64.RawURLEncoding.EncodeToString(b)
	}
	if len(enc) > sigInlineMax && store != nil {
		kind, enc = sigKept, store.keep(sig)
	}
	return id + sigMark + kind + strconv.Itoa(len(enc)) + sigEnd + enc
}

// unsignedID splits an id signedID made into the call's own id and its
// signature. The mark comes off whenever there is one, so the upstream
// sees the call's own id even when the signature is lost (cut short, or a
// key this store does not have); an id without a mark comes back as it is.
func unsignedID(id string, store *sigStore) (own, sig string) {
	for at := 1; at < len(id); {
		i := strings.Index(id[at:], sigMark)
		if i < 0 {
			break
		}
		i += at
		at = i + 1
		rest := id[i+len(sigMark):]
		raw := strings.HasPrefix(rest, sigRaw)
		kept := strings.HasPrefix(rest, sigKept)
		if raw || kept {
			rest = rest[1:]
		}
		d := 0
		for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
			d++
		}
		enc, ok := strings.CutPrefix(rest[d:], sigEnd)
		if !ok || d == 0 {
			continue // not a mark: part of the call's own id
		}
		own = id[:i]
		if rest[:d] != strconv.Itoa(len(enc)) {
			return own, "" // cut short on the way
		}
		if kept {
			return own, store.get(enc)
		}
		b, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil || len(b) == 0 {
			return own, ""
		}
		if raw {
			return own, string(b)
		}
		return own, base64.StdEncoding.EncodeToString(b)
	}
	return id, ""
}

// sigStore keeps the long signatures of one seller's provider in memory,
// the least recently used going first once it holds sigStoreMax of them
// or sigStoreBytes in all.
type sigStore struct {
	mu    sync.Mutex
	order *lru.List // of *sigEntry, the most recently used first
	byKey map[string]*lru.Element
	bytes int
}

type sigEntry struct{ key, sig string }

var (
	sigStoreMax   = 4096
	sigStoreBytes = 32 << 20
)

var sigStores = struct {
	sync.Mutex
	m map[string]*sigStore
}{m: map[string]*sigStore{}}

// signatureStore is the store of the provider with id entry: a key one
// provider gave means nothing to another, whose account did not sign it.
func signatureStore(entry string) *sigStore {
	sigStores.Lock()
	defer sigStores.Unlock()
	s := sigStores.m[entry]
	if s == nil {
		s = &sigStore{order: lru.New(), byKey: map[string]*lru.Element{}}
		sigStores.m[entry] = s
	}
	return s
}

func sigKey(sig string) string {
	h := sha256.Sum256([]byte(sig))
	return base64.RawURLEncoding.EncodeToString(h[:sigKeyBytes])
}

// keep stores sig and returns the key an id names it by.
func (s *sigStore) keep(sig string) string {
	key := sigKey(sig)
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.byKey[key]; ok {
		s.order.MoveToFront(el)
		return key
	}
	s.byKey[key] = s.order.PushFront(&sigEntry{key, sig})
	s.bytes += len(sig)
	for s.order.Len() > sigStoreMax || (s.bytes > sigStoreBytes && s.order.Len() > 1) {
		last := s.order.Back()
		e := last.Value.(*sigEntry)
		s.order.Remove(last)
		delete(s.byKey, e.key)
		s.bytes -= len(e.sig)
	}
	return key
}

// get is the signature kept under key, or "" when the store has none.
func (s *sigStore) get(key string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.byKey[key]
	if !ok {
		return ""
	}
	s.order.MoveToFront(el)
	return el.Value.(*sigEntry).sig
}
