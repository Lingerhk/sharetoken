package buyer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"sharetoken/internal/protocol"
)

// A request too long for the model is something each client acts on, by
// compacting the conversation, only when it is told in its own words (after
// magpie's tooLong and compactsInStream):
//
//   - Codex compacts only on a response.failed event whose error code is
//     context_length_exceeded, in the stream, even when no stream was sent
//     yet; a 400 or 502 ends the turn, or is retried five times;
//   - Claude Code compacts on an invalid_request_error that says "prompt is
//     too long";
//   - chat clients read the code context_length_exceeded.
//
// A rate limit counted in tokens ("tokens per minute") is no such refusal:
// told it is, a client compacts again and again; nor is one about the
// answer's own limit (max_tokens), which compacting does not help.

// CodeContextLength is the code a client compacts on.
const CodeContextLength = "context_length_exceeded"

var (
	tooLongPattern = regexp.MustCompile(`(?i)context_length_exceeded|context_too_large|prompt is too long|input is too long|prompt too long|(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|context (length|limit|window) (exceeded|is exceeded)|context (window )?(is )?full\b|maximum context length|too many (input |prompt )?tokens|input token count.*exceeds the maximum|reduce the length of the messages|longer than the model'?s? context length|greater than the context length|prompt token count of [\d,]+ exceeds the limit|exceeded model token limit|prompt exceeds max length|context_window_exceeded|token limit exceeded|上下文(长度)?(超|过长)|超(过|出)(了)?(模型)?(的)?(最大)?上下文|(内容|输入|提示词?|prompt)(长度)?\s*(太长|过长|超长)`)
	pacePattern    = regexp.MustCompile(`(?i)rate.?limit|per (minute|hour|day)|\bTP[MD]\b|please wait|try again later|throttl|速率|频繁|限流|每(秒|分钟|小时|天|日)`)
)

// tooLong reports whether err is the model refusing a request too long for
// its context window.
func tooLong(err error) bool {
	if err == nil {
		return false
	}
	switch statusFor(err) {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
	default:
		if codeFor(err) != protocol.CodeBadRequest {
			return false
		}
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "max_tokens") || strings.Contains(low, "max_output_tokens") || strings.Contains(low, "max_completion_tokens") {
		return false
	}
	return !pacePattern.MatchString(msg) && tooLongPattern.MatchString(msg)
}

// tooLongText is msg as Claude Code recognizes it.
func tooLongText(msg string) string {
	if strings.Contains(strings.ToLower(msg), "prompt is too long") {
		return msg
	}
	return "prompt is too long: " + msg
}

// tooLongFailed is the Responses event Codex compacts on.
func tooLongFailed(msg string) []byte {
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	b, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{
		"id": "resp_" + hex.EncodeToString(id), "object": "response", "status": "failed",
		"error": map[string]any{"code": CodeContextLength, "message": msg}, "output": []any{}}})
	return b
}

// writeTooLong answers a refused too-long request before any output: a
// streaming Responses client in its stream, the others with a 400 in their
// API's shape.
func writeTooLong(w http.ResponseWriter, api string, stream bool, msg string) {
	if api == protocol.APIResponses && stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.failed\ndata: "))
		_, _ = w.Write(tooLongFailed(msg))
		_, _ = w.Write([]byte("\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	if api == protocol.APIMessages {
		writeAPIError(w, api, http.StatusBadRequest, tooLongText(msg), CodeContextLength)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "code": CodeContextLength, "param": "messages"}})
	_, _ = w.Write(b)
}

// tooLongEvent is the event that ends a stream already under way with the
// refusal, in api's shape.
func tooLongEvent(api, msg string) []byte {
	switch api {
	case protocol.APIResponses:
		return tooLongFailed(msg)
	case protocol.APIMessages:
		return anthropicErrorJSON(http.StatusBadRequest, tooLongText(msg))
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "code": CodeContextLength}})
	return b
}
