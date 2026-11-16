// Package apicompat converts between the three model APIs sharetoken serves:
// OpenAI chat completions ("chat"), Anthropic Messages ("messages") and
// OpenAI Responses ("responses").
//
// A buyer sends its request in the API its tool speaks; the node passes it
// through when its upstream speaks the same API, and converts it here when it
// does not: the request into the upstream's API, and the upstream's streamed
// events back into the buyer's. The design follows sub2api's apicompat
// package (LGPL-3.0, used as a reference for behaviour and test cases only).
//
// Everything works on streams. A converted request always asks the upstream
// to stream; a buyer that wants one JSON answer gets the events aggregated on
// its own side (Aggregate).
//
// Event payloads are the JSON after "data:" of one server-sent event. A
// Messages or Responses payload names its event in its "type" field, so the
// "event:" line is not kept: Event restores it.
package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The APIs.
const (
	Chat      = "chat"
	Messages  = "messages"
	Responses = "responses"
)

// APIs lists every API, in a fixed order.
var APIs = []string{Chat, Messages, Responses}

// Valid reports whether api is one of the APIs.
func Valid(api string) bool { return api == Chat || api == Messages || api == Responses }

// Translator turns an upstream's streamed events into the client's API. It is
// used by one request only, from one goroutine.
type Translator interface {
	// Event translates one upstream event payload and calls emit with each
	// client event payload it produces (zero or more).
	Event(payload []byte, emit func([]byte) error) error
	// Close is called once the upstream stream ended without an error. It
	// emits the closing events the client still lacks (a stream the upstream
	// cut short is closed as if it had stopped there), and fails when the
	// upstream produced nothing the client could use.
	Close(emit func([]byte) error) error
}

// Conversion is a request turned into an upstream API.
type Conversion struct {
	Body []byte // the upstream request; it asks for a stream
	// Translator turns the upstream's events back into the client's API.
	Translator Translator
}

// Options carry what a conversion may need besides the body.
type Options struct {
	// Model is the client-facing model name, used in the events the client
	// gets (the body carries the upstream's name for it).
	Model string
}

// converter converts a request from one API to another.
type converter func(body []byte, opt Options) (*Conversion, error)

var converters = map[[2]string]converter{}

// register adds the converter from one API to another; files call it from init.
func register(from, to string, c converter) { converters[[2]string{from, to}] = c }

// Convert turns body, a request in API from, into a request in API to, and
// returns how to translate the upstream's events back.
func Convert(from, to string, body []byte, opt Options) (*Conversion, error) {
	c := converters[[2]string{from, to}]
	if c == nil {
		return nil, fmt.Errorf("apicompat: no conversion from %s to %s", from, to)
	}
	return c(body, opt)
}

// Supported reports whether a conversion from one API to another exists.
func Supported(from, to string) bool { return converters[[2]string{from, to}] != nil }

// Target picks the upstream API to convert a request in api to, among the
// APIs an upstream speaks: the same API when it can, else the closest one
// (the one that can carry the most of the request: Responses and Messages both
// carry reasoning and tool items that chat cannot).
func Target(api string, upstream []string) (string, bool) {
	order := map[string][]string{
		Chat:      {Chat, Responses, Messages},
		Messages:  {Messages, Responses, Chat},
		Responses: {Responses, Messages, Chat},
	}[api]
	for _, want := range order {
		for _, have := range upstream {
			if have == want && (want == api || Supported(api, want)) {
				return want, true
			}
		}
	}
	return "", false
}

// aggregator builds the non-streaming answer of an API from its events.
type aggregator func(payloads [][]byte) ([]byte, error)

var aggregators = map[string]aggregator{}

func registerAggregator(api string, a aggregator) { aggregators[api] = a }

// Aggregate builds the JSON answer a non-streaming client of api expects from
// the stream's event payloads, in order.
func Aggregate(api string, payloads [][]byte) ([]byte, error) {
	a := aggregators[api]
	if a == nil {
		return nil, fmt.Errorf("apicompat: cannot aggregate %s", api)
	}
	return a(payloads)
}

// EventName is the SSE event name of a Messages or Responses payload: its
// "type" field. Chat chunks have none.
func EventName(api string, payload []byte) string {
	if api == Chat {
		return ""
	}
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &head) != nil {
		return ""
	}
	return head.Type
}

// repeatedCallID is the ID the n-th (from 2) tool call with the same ID gets
// where an upstream wants call IDs unique in a request; its result gets the
// same one.
func repeatedCallID(id string, n int) string { return id + "_" + strconv.Itoa(n) }

// IsConverted reports whether an opaque value (a reasoning item's
// encrypted_content, a thinking block's signature) was made by sharetoken's
// conversions rather than by a model server: only the conversions read it,
// and a model server given it refuses the request.
func IsConverted(opaque string) bool { return strings.HasPrefix(opaque, "sharetoken-") }

// Compaction. Codex shrinks a long conversation with POST /responses/compact,
// whose answer is one "compaction" item with an opaque encrypted_content that
// later requests send back in their input. Only the Codex backend can read
// its own compaction items. An upstream without that endpoint gets a normal
// request that asks the model for a summary, and the answer is a compaction
// item that carries the summary itself, marked with CompactionPrefix; a
// later request's compaction item becomes that summary again, as a user
// message, for whichever upstream it goes to.

// CompactionPrefix marks a compaction item's encrypted_content made by
// sharetoken: the rest is the summary text, base64 (standard, padded).
const CompactionPrefix = "sharetoken-compaction-v1:"

// EncodeCompaction is the encrypted_content of a compaction item holding summary.
func EncodeCompaction(summary string) string {
	return CompactionPrefix + base64.StdEncoding.EncodeToString([]byte(summary))
}

// DecodeCompaction returns the summary of a compaction item made by
// sharetoken, and false for one made by an upstream (opaque).
func DecodeCompaction(encrypted string) (string, bool) {
	rest, ok := strings.CutPrefix(encrypted, CompactionPrefix)
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// SummaryMessage is the user message a compaction summary becomes.
func SummaryMessage(summary string) string {
	return "<conversation_summary>\n" + summary + "\n</conversation_summary>"
}

// CompactPrompt asks a model for the summary a compaction item holds. It is
// the last user message of the request that stands in for /responses/compact.
const CompactPrompt = `Your task is to produce a faithful, concise summary of the conversation so far so that a successor assistant can continue the work seamlessly after the earlier turns are discarded. The successor will see the user's original query plus this summary. Capture what is needed to continue: the user's explicit requests, your most recent actions, key technical details, file paths, commands, configuration, and decisions. Prefer tight prose and short references over long verbatim dumps.

If earlier turns include a prior summary (inside <conversation_summary> tags), treat it as authoritative for the early history and carry its still-relevant information forward.

Organize the summary into these sections, writing "None" for an empty one:
1. Primary request and intent
2. Key technical concepts
3. Files and code sections (paths and the relevant code, the latest edits in full)
4. Errors and fixes
5. Problem solving
6. All user messages (not tool results), in order; leave out this summarization request
7. Pending tasks
8. Current work: exactly what was being done right before this summary
9. Optional next step, strictly in line with the user's latest request

Do not call any tool. Answer with only the summary inside one <summary>...</summary> block.`
