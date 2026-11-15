package protocol

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Prompt estimates of the Messages and Responses APIs, and of what the text
// estimate cannot see: images and files. The estimate bounds what a node may
// bill (UsagePlausible), so it errs on the high side for attachments.

const (
	imageTokens = 1600 // a large image under Claude's and OpenAI's tiling
	fileTokens  = 1600 // a file's fixed part; its bytes add more
)

// estimateMessages estimates an Anthropic Messages body.
func estimateMessages(body []byte) (uint64, error) {
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, err
	}
	var e estimator
	e.raw(req.System)
	for _, m := range req.Messages {
		e.n += perMessageOverhead
		e.raw(m.Content)
	}
	if len(req.Tools) > 0 {
		e.n += EstimateTokens(string(req.Tools))
	}
	return e.total(), nil
}

// estimateResponses estimates an OpenAI Responses body.
func estimateResponses(body []byte) (uint64, error) {
	var req struct {
		Instructions json.RawMessage `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		Tools        json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, err
	}
	var e estimator
	e.raw(req.Instructions)
	var items []json.RawMessage
	if json.Unmarshal(req.Input, &items) == nil {
		for _, it := range items {
			e.n += perMessageOverhead
			e.raw(it)
		}
	} else {
		e.raw(req.Input)
	}
	if len(req.Tools) > 0 {
		e.n += EstimateTokens(string(req.Tools))
	}
	return e.total(), nil
}

// attachmentTokens is the estimate for the images and files among a chat
// message's content parts.
func attachmentTokens(raw json.RawMessage) uint64 {
	var parts []any
	if len(raw) == 0 || json.Unmarshal(raw, &parts) != nil {
		return 0
	}
	var e estimator
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			e.attachment(m)
		}
	}
	return e.n
}

// estimator walks content of any of the APIs: strings are text, objects are
// parts, items or blocks whose text fields count and whose attachments are
// estimated by size.
type estimator struct {
	n    uint64
	text strings.Builder
}

func (e *estimator) total() uint64 { return e.n + EstimateTokens(e.text.String()) }

func (e *estimator) raw(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		e.walk(v, 0)
	}
}

func (e *estimator) walk(v any, depth int) {
	if depth > 16 {
		return
	}
	switch v := v.(type) {
	case string:
		e.text.WriteString(v)
	case []any:
		for _, x := range v {
			e.walk(x, depth+1)
		}
	case map[string]any:
		if e.attachment(v) {
			return
		}
		for _, k := range []string{"text", "thinking", "arguments", "input", "output", "content", "summary", "data", "encrypted_content"} {
			x, ok := v[k]
			if !ok {
				continue
			}
			switch k {
			case "input":
				// A tool call's input is an object; Responses' input is items.
				if _, isMap := x.(map[string]any); isMap {
					b, _ := json.Marshal(x)
					e.text.Write(b)
					continue
				}
			case "data":
				// A text document's data; base64 data is an attachment.
				if v["type"] != "text" {
					continue
				}
			case "encrypted_content":
				// Reasoning items passed back count as input upstream. A
				// compaction item made by sharetoken holds its summary.
				if s, ok := x.(string); ok {
					if v["type"] == "compaction" || v["type"] == "compaction_summary" {
						if sum, ok := decodeCompactionSummary(s); ok {
							e.text.WriteString(sum)
							continue
						}
					}
					e.n += uint64(len(s)) / 4
				}
				continue
			}
			e.walk(x, depth+1)
		}
	}
}

// attachment counts an image or file part and reports whether m is one.
func (e *estimator) attachment(m map[string]any) bool {
	typ, _ := m["type"].(string)
	switch typ {
	case "image", "image_url", "input_image":
		e.n += imageTokens
		return true
	case "document", "file", "input_file", "input_audio":
		e.n += fileTokens + uint64(base64Size(m))/3
		if src, ok := m["source"].(map[string]any); ok && src["type"] == "text" {
			if s, ok := src["data"].(string); ok {
				e.text.WriteString(s)
			}
		}
		return true
	}
	return false
}

// base64Size finds the longest string in a part (its base64 data) and
// returns the size of what it encodes.
func base64Size(v any) int {
	best := 0
	var walk func(any, int)
	walk = func(v any, d int) {
		if d > 4 {
			return
		}
		switch v := v.(type) {
		case string:
			best = max(best, len(v)*3/4)
		case map[string]any:
			for _, x := range v {
				walk(x, d+1)
			}
		}
	}
	walk(v, 0)
	return best
}

// decodeCompactionSummary reads a compaction item made by sharetoken (see
// apicompat.EncodeCompaction).
func decodeCompactionSummary(s string) (string, bool) {
	const prefix = "sharetoken-compaction-v1:"
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return "", false
	}
	b, err := decodeStdBase64(rest)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func decodeStdBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
