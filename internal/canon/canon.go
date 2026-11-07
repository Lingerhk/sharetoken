// Package canon is the deterministic, length-prefixed encoding used as the
// input to every signature in sharetoken (receipts, offers, tickets, acks).
//
// Two rules make it safe to sign:
//   - Every variable-length field is prefixed with its 4-byte big-endian length,
//     so ("ab","c") and ("a","bc") encode differently.
//   - Every encoding starts with a domain string naming the message type and
//     version, so a signature over one message type can never be replayed as a
//     valid signature over another.
//
// JSON is used on the wire for convenience, but it is never signed directly:
// JSON has no canonical form.
package canon

import "encoding/binary"

// Encoder accumulates a canonical encoding.
type Encoder struct{ b []byte }

// New starts an encoding whose first field is the domain separator.
func New(domain string) *Encoder {
	e := &Encoder{b: make([]byte, 0, 256)}
	return e.String(domain)
}

// Bytes appends a length-prefixed byte string.
func (e *Encoder) Bytes(p []byte) *Encoder {
	e.b = binary.BigEndian.AppendUint32(e.b, uint32(len(p)))
	e.b = append(e.b, p...)
	return e
}

// String appends a length-prefixed string.
func (e *Encoder) String(s string) *Encoder {
	e.b = binary.BigEndian.AppendUint32(e.b, uint32(len(s)))
	e.b = append(e.b, s...)
	return e
}

// Strings appends a count followed by each string.
func (e *Encoder) Strings(ss []string) *Encoder {
	e.U64(uint64(len(ss)))
	for _, s := range ss {
		e.String(s)
	}
	return e
}

// U64 appends a fixed 8-byte big-endian integer.
func (e *Encoder) U64(v uint64) *Encoder {
	e.b = binary.BigEndian.AppendUint64(e.b, v)
	return e
}

// I64 appends a fixed 8-byte two's-complement integer.
func (e *Encoder) I64(v int64) *Encoder { return e.U64(uint64(v)) }

// Bool appends a single byte, 1 or 0.
func (e *Encoder) Bool(v bool) *Encoder {
	if v {
		e.b = append(e.b, 1)
	} else {
		e.b = append(e.b, 0)
	}
	return e
}

// Sum returns the encoding.
func (e *Encoder) Sum() []byte { return e.b }
