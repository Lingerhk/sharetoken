package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/network"

	"sharetoken/internal/version"
)

// MaxFrameSize bounds a single frame so a peer cannot make us allocate
// unbounded memory with a forged length prefix. Only a sealed request comes
// anywhere near it: up to MaxRequestBody of JSON, compressed before sealing.
const MaxFrameSize = 8 << 20

// MaxSmallFrame bounds frames whose contents are a few fields: relay passes,
// acks, rejects. A handler that expects one of these reads with this limit so
// a peer cannot even start sending a large frame.
const MaxSmallFrame = 64 << 10

// recvChunk is how much Recv reads from the stream at a time while a frame
// arrives. Memory grows with the bytes actually received, so a peer that
// announces an 8 MiB frame and then stalls holds only the header it sent.
const recvChunk = 64 << 10

// A frame is a 4-byte big-endian length and a payload. A JSON frame's payload
// is an Envelope, and so begins with '{'. A binary frame's payload begins with
// one of the Bin* kinds below (all below 0x20, so never '{'), followed by its
// bytes. The data plane sends its bulk in binary frames: no JSON, no base64.
const (
	BinRequest  byte = 0x01 // the sealed request (EncodeRequest, box.SealFrom)
	BinData     byte = 0x02 // uvarint sequence number, then one sealed response frame
	BinProgress byte = 0x03 // empty; the node is alive while the model is silent
)

// Frame is one frame read from a stream: either Env (a JSON frame) or a
// binary frame of kind Kind with Payload.
type Frame struct {
	Env     *Envelope
	Kind    byte
	Payload []byte
}

// Envelope is one frame on a stream: a type tag and a JSON body.
type Envelope struct {
	Type string          `json:"type"`
	Ver  string          `json:"ver,omitempty"` // the sender's version, on the frame that opens a request
	Body json.RawMessage `json:"body,omitempty"`
}

// Send writes one frame: a 4-byte big-endian length followed by the JSON
// envelope, in a single Write so frames never interleave.
func Send(w io.Writer, typ string, body any) error { return send(w, typ, "", body) }

// SendRequest is Send for the frame that opens a request: it says which version
// is asking. Receivers refuse a request without it.
func SendRequest(w io.Writer, typ string, body any) error {
	return send(w, typ, version.Version, body)
}

func send(w io.Writer, typ, ver string, body any) error {
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = b
	}
	payload, err := json.Marshal(Envelope{Type: typ, Ver: ver, Body: raw})
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

// SendBinary writes one binary frame of kind made of the concatenated parts,
// in a single Write.
func SendBinary(w io.Writer, kind byte, parts ...[]byte) error {
	n := 1
	for _, p := range parts {
		n += len(p)
	}
	if n > MaxFrameSize {
		return fmt.Errorf("protocol: frame of %d bytes exceeds limit", n)
	}
	buf := make([]byte, 4, 4+n)
	binary.BigEndian.PutUint32(buf, uint32(n))
	buf = append(buf, kind)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	_, err := w.Write(buf)
	return err
}

// SendData writes a BinData frame: seq, then the sealed bytes.
func SendData(w io.Writer, seq uint64, sealed []byte) error {
	return SendBinary(w, BinData, binary.AppendUvarint(nil, seq), sealed)
}

// DecodeData splits a BinData payload into its sequence number and sealed bytes.
func DecodeData(payload []byte) (uint64, []byte, error) {
	seq, n := binary.Uvarint(payload)
	if n <= 0 {
		return 0, nil, errors.New("protocol: bad data frame")
	}
	return seq, payload[n:], nil
}

// Recv reads one JSON frame of up to MaxFrameSize bytes.
func Recv(r io.Reader) (*Envelope, error) { return RecvLimit(r, MaxFrameSize) }

// RecvLimit reads one JSON frame of up to max bytes. The payload buffer grows
// as the bytes arrive rather than being allocated from the announced length.
func RecvLimit(r io.Reader, max int) (*Envelope, error) {
	f, err := ReadFrame(r, max)
	if err != nil {
		return nil, err
	}
	return f.envelope()
}

// ReadFrame reads one frame, JSON or binary, of up to max bytes.
func ReadFrame(r io.Reader, max int) (*Frame, error) {
	n, err := recvHeader(r, max)
	if err != nil {
		return nil, err
	}
	return recvFrame(r, n)
}

// envelope returns a JSON frame's envelope, or an error for a binary frame.
func (f *Frame) envelope() (*Envelope, error) {
	if f.Env == nil {
		return nil, fmt.Errorf("protocol: unexpected binary frame of kind %d", f.Kind)
	}
	return f.Env, nil
}

// RecvStream is RecvLimit on a libp2p stream: before reading the payload it
// reserves its size with the stream's resource scope, so that the resource
// manager's per-stream, per-peer and system memory limits bound what all
// peers together can have in flight. A peer that exceeds them gets an error
// and the frame is not read. The reservation is released once the frame is
// parsed; the handler's own use of the body is short compared with the time
// a peer can spend sending it.
func RecvStream(s network.Stream, max int) (*Envelope, error) {
	f, err := ReadStreamFrame(s, max)
	if err != nil {
		return nil, err
	}
	return f.envelope()
}

// ReadStreamFrame is ReadFrame on a libp2p stream, with RecvStream's memory
// reservation.
func ReadStreamFrame(s network.Stream, max int) (*Frame, error) {
	n, err := recvHeader(s, max)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		if err := s.Scope().ReserveMemory(n, network.ReservationPriorityLow); err != nil {
			return nil, fmt.Errorf("protocol: frame of %d bytes refused: %w", n, err)
		}
		defer s.Scope().ReleaseMemory(n)
	}
	return recvFrame(s, n)
}

func recvHeader(r io.Reader, max int) (int, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if max > MaxFrameSize || max <= 0 {
		max = MaxFrameSize
	}
	if int64(n) > int64(max) {
		return 0, fmt.Errorf("protocol: incoming frame of %d bytes exceeds limit of %d", n, max)
	}
	return int(n), nil
}

func recvFrame(r io.Reader, n int) (*Frame, error) {
	var buf bytes.Buffer
	buf.Grow(min(n, recvChunk))
	for buf.Len() < n {
		step := min(n-buf.Len(), recvChunk)
		if _, err := io.CopyN(&buf, r, int64(step)); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	b := buf.Bytes()
	if len(b) > 0 && b[0] < 0x20 {
		return &Frame{Kind: b[0], Payload: b[1:]}, nil
	}
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("protocol: bad frame: %w", err)
	}
	return &Frame{Env: &env}, nil
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
