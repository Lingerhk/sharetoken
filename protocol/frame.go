package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxFrameSize bounds a single frame so a peer cannot make us allocate
// unbounded memory with a forged length prefix.
const MaxFrameSize = 8 << 20

// Envelope is one frame on a stream: a type tag and a JSON body.
type Envelope struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Send writes one frame: a 4-byte big-endian length followed by the JSON
// envelope, in a single Write so frames never interleave.
func Send(w io.Writer, typ string, body any) error {
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = b
	}
	payload, err := json.Marshal(Envelope{Type: typ, Body: raw})
	if err != nil {
		return err
	}
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("protocol: frame of %d bytes exceeds limit", len(payload))
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	copy(buf[4:], payload)
	_, err = w.Write(buf)
	return err
}

// SendError writes an error frame. Errors that are not *Error are sent as a
// generic internal error so implementation details do not leak to peers.
func SendError(w io.Writer, err error) error {
	var pe *Error
	if !errors.As(err, &pe) {
		pe = &Error{Code: CodeInternal, Message: "internal error"}
	}
	return Send(w, TypeError, pe)
}

// Recv reads one frame.
func Recv(r io.Reader) (*Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("protocol: incoming frame of %d bytes exceeds limit", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("protocol: bad frame: %w", err)
	}
	return &env, nil
}

// Decode checks the frame type and unmarshals the body into v. An error frame
// is returned as *Error.
func (e *Envelope) Decode(want string, v any) error {
	if e.Type == TypeError {
		var pe Error
		if err := json.Unmarshal(e.Body, &pe); err != nil {
			return errors.New("protocol: unreadable error frame")
		}
		return &pe
	}
	if e.Type != want {
		return fmt.Errorf("protocol: expected %q frame, got %q", want, e.Type)
	}
	if v == nil || len(e.Body) == 0 {
		return nil
	}
	return json.Unmarshal(e.Body, v)
}
