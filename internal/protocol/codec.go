package protocol

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"sharetoken/internal/canon"
)

// What goes inside the sealed boxes of the infer protocol.
//
// The request is compressed (DEFLATE with a preset dictionary) and padded to a
// size bucket before it is sealed, so the relay sees neither the prompt's
// redundancy nor its exact length.
//
// The response is one DEFLATE stream for the whole job. The node writes each
// upstream SSE payload into it as a record (uvarint length, then the bytes),
// and from time to time flushes the stream into a frame: the compressed bytes
// since the last frame (ending in a sync flush, whose fixed 00 00 ff ff tail
// is left out), padded to a size bucket, then sealed under the job's response
// stream key with the frame's sequence number. Several payloads that arrive
// close together share a frame, and repeated JSON keys, ids and model names
// cost almost nothing after the first one. The last frame closes the stream.
//
// Every frame holds whole records, so the buyer decodes each frame on arrival:
// a fresh decompressor primed with the last 32 KiB of output (all DEFLATE can
// refer back to) continues where the previous frame stopped.

// MaxRequestBody bounds a request's JSON before compression (the local API's
// limit) and, on the node, after decompression: 32 MiB, the Messages limit of
// most clouds that serve Claude (Anthropic's own API takes 128 MB).
const MaxRequestBody = 32 << 20

// MaxSealedRequest bounds the sealed request on the wire: MaxRequestBody that
// does not compress (images in base64), padded, plus the box.
const MaxSealedRequest = MaxRequestBody + MaxRequestBody/4 + 1<<20

// RequestFrameSize is the size of the BinRequest frames a sealed request is
// sent in; each is reserved with the resource manager on its own.
const RequestFrameSize = 1 << 20

// codecDict primes both compressors: the JSON that every request and every
// streamed chunk repeats.
var codecDict = []byte(`{"model":"","stream":true,"stream_options":{"include_usage":true},"max_tokens":,"temperature":,"tools":[{"type":"function","function":{"name":"","description":"","parameters":{"type":"object","properties":{"type":"string"}},"required":[]}}],"messages":[{"role":"system","content":""},{"role":"user","content":[{"type":"text","text":""}]},{"role":"assistant","content":null,"tool_calls":[{"id":"call_","type":"function","function":{"name":"","arguments":""}}]},{"role":"tool","tool_call_id":"","content":""}],"session_id":""}` +
	`{"id":"chatcmpl-","object":"chat.completion.chunk","created":,"model":"","system_fingerprint":"fp_","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"","content":""},"logprobs":null,"finish_reason":null}]}` +
	`{"choices":[],"usage":{"prompt_tokens":,"completion_tokens":,"total_tokens":,"prompt_tokens_details":{"cached_tokens":}}}"finish_reason":"stop"`)

const (
	codecDeflate = 1 // the only request encoding: DEFLATE with codecDict
	windowSize   = 32 << 10
	reqLevel     = 5
	respLevel    = 6
)

// syncTail ends every sync flush; frames leave it out.
var syncTail = []byte{0, 0, 0xff, 0xff}

// PadRequest is the size a request plaintext of n bytes is padded to: 1 KiB,
// then buckets growing by a quarter, so the length shows only within 25%.
func PadRequest(n int) int {
	b := 1024
	for b < n {
		b += (b/4 + 63) &^ 63
	}
	return b
}

// PadFrame is the size a response frame plaintext of n bytes is padded to:
// 32-byte steps up to 1 KiB, 256-byte steps beyond.
func PadFrame(n int) int {
	if n <= 1024 {
		return max((n+31)&^31, 32)
	}
	return (n + 255) &^ 255
}

// EncodeRequest compresses and pads a request's JSON for sealing.
func EncodeRequest(body []byte) ([]byte, error) {
	if len(body) > MaxRequestBody {
		return nil, Errorf(CodeBadRequest, "request of %d bytes exceeds the %d byte limit", len(body), MaxRequestBody)
	}
	var c bytes.Buffer
	w, err := flate.NewWriterDict(&c, reqLevel, codecDict)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(body); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	out := append([]byte{codecDeflate}, binary.AppendUvarint(nil, uint64(c.Len()))...)
	out = append(out, c.Bytes()...)
	return append(out, make([]byte, PadRequest(len(out))-len(out))...), nil
}

// DecodeRequest reverses EncodeRequest, refusing more than MaxRequestBody.
func DecodeRequest(plain []byte) ([]byte, error) {
	if len(plain) < 2 || plain[0] != codecDeflate {
		return nil, errors.New("protocol: unknown request encoding")
	}
	n, k := binary.Uvarint(plain[1:])
	if k <= 0 || n > uint64(len(plain)-1-k) {
		return nil, errors.New("protocol: bad request length")
	}
	c := plain[1+k : 1+k+int(n)]
	if !allZero(plain[1+k+int(n):]) {
		return nil, errors.New("protocol: bad request padding")
	}
	body, err := io.ReadAll(io.LimitReader(flate.NewReaderDict(bytes.NewReader(c), codecDict), MaxRequestBody+1))
	if err != nil {
		return nil, fmt.Errorf("protocol: request does not decompress: %w", err)
	}
	if len(body) > MaxRequestBody {
		return nil, Errorf(CodeBadRequest, "request exceeds the %d byte limit", MaxRequestBody)
	}
	return body, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// ResponseKeyInfo binds a job's response stream key (box.Identity.NewStream)
// to the job and to both keys of the agreement.
func ResponseKeyInfo(t *Ticket, buyerBoxPub []byte) []byte {
	return canon.New("sharetoken/stream/response/v1").String(t.JobID).Bytes(buyerBoxPub).Bytes(t.NodeBoxPub).Sum()
}

// ResponseEncoder builds a job's response frames. It is not safe for
// concurrent use.
type ResponseEncoder struct {
	out     bytes.Buffer
	w       *flate.Writer
	pending int // records written since the last frame
	records int
}

// NewResponseEncoder starts a response stream.
func NewResponseEncoder() *ResponseEncoder {
	e := &ResponseEncoder{}
	e.w, _ = flate.NewWriterDict(&e.out, respLevel, codecDict)
	return e
}

// Write adds one upstream payload as a record.
func (e *ResponseEncoder) Write(payload []byte) error {
	if _, err := e.w.Write(binary.AppendUvarint(nil, uint64(len(payload)))); err != nil {
		return err
	}
	if _, err := e.w.Write(payload); err != nil {
		return err
	}
	e.pending++
	e.records++
	return nil
}

// Pending reports how many records the next frame will carry.
func (e *ResponseEncoder) Pending() int { return e.pending }

// Records reports how many records were written in all.
func (e *ResponseEncoder) Records() int { return e.records }

// Frame flushes the records written since the last frame into a padded frame
// plaintext; with final it also ends the stream.
func (e *ResponseEncoder) Frame(final bool) ([]byte, error) {
	var err error
	if final {
		err = e.w.Close()
	} else {
		err = e.w.Flush()
	}
	if err != nil {
		return nil, err
	}
	c := e.out.Bytes()
	if !final {
		if !bytes.HasSuffix(c, syncTail) {
			return nil, errors.New("protocol: flush did not end in a sync marker")
		}
		c = c[:len(c)-len(syncTail)]
	}
	out := binary.AppendUvarint(make([]byte, 0, PadFrame(len(c)+3)), uint64(len(c)))
	out = append(out, c...)
	out = append(out, make([]byte, PadFrame(len(out))-len(out))...)
	e.out.Reset()
	e.pending = 0
	return out, nil
}

// ResponseDecoder reads a job's response frames in order.
type ResponseDecoder struct {
	window []byte // the last windowSize bytes of output, or the dictionary
	r      io.ReadCloser
	done   bool
}

// NewResponseDecoder starts reading a response stream.
func NewResponseDecoder() *ResponseDecoder {
	return &ResponseDecoder{window: bytes.Clone(codecDict)}
}

// Done reports whether the final frame was read.
func (d *ResponseDecoder) Done() bool { return d.done }

// Frame decodes one frame plaintext into the records it carries. An error
// means the frame is malformed: the stream cannot continue.
func (d *ResponseDecoder) Frame(plain []byte) ([][]byte, error) {
	if d.done {
		return nil, errors.New("protocol: frame after the end of the response")
	}
	n, k := binary.Uvarint(plain)
	if k <= 0 || n > uint64(len(plain)-k) || !allZero(plain[k+int(n):]) {
		return nil, errors.New("protocol: bad frame padding")
	}
	src := io.MultiReader(bytes.NewReader(plain[k:k+int(n)]), bytes.NewReader(syncTail))
	if d.r == nil {
		d.r = flate.NewReaderDict(src, d.window)
	} else if err := d.r.(flate.Resetter).Reset(src, d.window); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(d.r, MaxFrameSize))
	switch {
	case err == nil:
		// The stream's final block: the sync tail appended is never read.
		d.done = true
	case errors.Is(err, io.ErrUnexpectedEOF):
		// Input ends after the sync flush: more frames follow.
	default:
		return nil, fmt.Errorf("protocol: frame does not decompress: %w", err)
	}
	if len(out) >= windowSize {
		d.window = bytes.Clone(out[len(out)-windowSize:])
	} else {
		d.window = append(d.window, out...)
		if len(d.window) > windowSize {
			d.window = d.window[len(d.window)-windowSize:]
		}
	}
	var recs [][]byte
	for len(out) > 0 {
		l, k := binary.Uvarint(out)
		if k <= 0 || l > uint64(len(out)-k) {
			return nil, errors.New("protocol: frame ends inside a record")
		}
		recs = append(recs, out[k:k+int(l)])
		out = out[k+int(l):]
	}
	return recs, nil
}

// SendSealedRequest writes a sealed request as BinRequest frames of at most
// RequestFrameSize bytes. The header before it says how many bytes follow.
func SendSealedRequest(w io.Writer, sealed []byte) error {
	for len(sealed) > 0 {
		n := min(len(sealed), RequestFrameSize)
		if err := SendBinary(w, BinRequest, sealed[:n]); err != nil {
			return err
		}
		sealed = sealed[n:]
	}
	return nil
}

// FrameReader reads one frame: ReadFrame, or ReadStreamFrame on a stream.
type FrameReader func(max int) (*Frame, error)

// ReadSealedRequest reads the n bytes of a sealed request that
// SendSealedRequest wrote. before, if set, runs ahead of each frame (to move
// a read deadline: a large request may take minutes on a slow link, but no
// single frame should).
func ReadSealedRequest(read FrameReader, n uint64, before func()) ([]byte, error) {
	if n == 0 || n > MaxSealedRequest {
		return nil, Errorf(CodeBadRequest, "a sealed request of %d bytes is not allowed (at most %d)", n, MaxSealedRequest)
	}
	out := make([]byte, 0, min(n, RequestFrameSize))
	for uint64(len(out)) < n {
		if before != nil {
			before()
		}
		f, err := read(RequestFrameSize + 16)
		if err != nil {
			return nil, err
		}
		if f.Env != nil || f.Kind != BinRequest {
			return nil, Errorf(CodeBadRequest, "expected the sealed request")
		}
		if uint64(len(out)+len(f.Payload)) > n {
			return nil, Errorf(CodeBadRequest, "the sealed request is longer than announced")
		}
		out = append(out, f.Payload...)
	}
	return out, nil
}
