package provider

import (
	"encoding/json"
	"regexp"
	"sync"
)

// Claude Code asks Anthropic's API for what its newest models take, and a
// model may not take it yet (after magpie's anthropic_shapes.go):
//
//   - thinking.display "updates" (and "highlights"): claude-sonnet-5-5 answers
//     "thinking.adaptive.display: Input should be 'summarized', 'omitted'";
//     an endpoint that knows no display at all answers "Extra inputs are not
//     permitted";
//   - the effort of each turn, as output_config on a {role: "system"} message
//     inside messages: "messages.1.output_config: Extra inputs are not
//     permitted" (Messages endpoints of other vendors).
//
// Claude Code mends either after the 400, once per conversation, by sending
// the request again without it; every conversation pays for one refused job.
// The adapter mends it instead: the request is sent again at once without
// what was refused, and the model is not sent it again while the seller runs.
// A model that takes them is sent them as the buyer wrote them.

type shapeFlags uint8

const (
	shapeDisplayNew shapeFlags = 1 << iota // a display other than summarized or omitted
	shapeDisplay                           // display at all
	shapeTurnEffort                        // output_config on a message
)

var (
	displayValueRefused = regexp.MustCompile(`thinking\.(?:adaptive|enabled)\.display:? Input should be`)
	displayRefused      = regexp.MustCompile(`thinking\.(?:adaptive|enabled)\.display:? Extra inputs are not permitted`)
	turnEffortRefused   = regexp.MustCompile(`messages\.\d+\.output_config:? Extra inputs are not permitted|Mid-conversation reasoning effort \(configuration_update\) is not supported`)
	// asksBetweenTools is Sonnet 5.5's answer to thinking turned off, for a
	// name ClaudeThinkingOffAsTaken does not know: "To turn thinking off on
	// this model, send "thinking": {"type": "between_tools"} instead of
	// {"type": "disabled"}", or "Use thinking.type.between_tools for the
	// lowest thinking setting".
	asksBetweenTools = regexp.MustCompile(`(?i)between_tools\W+instead of|use\W+thinking\.type\.between_tools`)
)

// anthropicShapes remembers, per model, the request shapes the model server
// refused.
type anthropicShapes struct {
	mu      sync.Mutex
	byModel map[string]shapeFlags
	// images is the most images a model took in one request, as its
	// refusal of more said (anthropic_images.go).
	images map[string]int
}

// imageCap is the most images model takes in one request, 0 for not known.
func (s *anthropicShapes) imageCap(model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.images[model]
}

// learnImageCap notes the image cap a refusal of req names; true when the
// request can go again within it (enough images before its latest user
// message to leave out).
func (s *anthropicShapes) learnImageCap(model, refusal string, req map[string]any) bool {
	n := refusedImageCap(refusal)
	if n == 0 {
		return false
	}
	refs := imageRefs(req)
	over := len(refs) - n
	if over <= 0 || earlierImages(req, refs) < over {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.images[model]; cur > 0 && cur <= n {
		return false
	}
	if s.images == nil {
		s.images = map[string]int{}
	}
	s.images[model] = n
	return true
}

func (s *anthropicShapes) get(model string) shapeFlags {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byModel[model]
}

// learn notes what of req (the request as it was sent) the model refused in
// refusal; true when there is something to leave out on the next try.
func (s *anthropicShapes) learn(model, refusal string, req map[string]any) bool {
	if s.learnImageCap(model, refusal, req) {
		return true
	}
	var add shapeFlags
	if th, _ := req["thinking"].(map[string]any); th != nil {
		if d, ok := th["display"].(string); ok {
			switch {
			case displayRefused.MatchString(refusal):
				add |= shapeDisplay
			case displayValueRefused.MatchString(refusal) && !knownDisplay(d):
				add |= shapeDisplayNew
			}
		}
	}
	if turnEffortRefused.MatchString(refusal) && hasTurnEffort(req) {
		add |= shapeTurnEffort
	}
	if add == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byModel == nil {
		s.byModel = map[string]shapeFlags{}
	}
	if s.byModel[model]&add == add {
		return false
	}
	s.byModel[model] |= add
	return true
}

// apply leaves out of req what the model refused before.
func (s *anthropicShapes) apply(model string, req map[string]any) {
	f := s.get(model)
	if f == 0 {
		return
	}
	if th, _ := req["thinking"].(map[string]any); th != nil {
		if d, ok := th["display"].(string); ok {
			switch {
			case f&shapeDisplay != 0:
				delete(th, "display")
			case f&shapeDisplayNew != 0 && !knownDisplay(d):
				// "highlights" still shows the thinking; "updates", or any
				// other, goes, as Claude Code itself sends it again.
				if d == "highlights" {
					th["display"] = "summarized"
				} else {
					delete(th, "display")
				}
			}
		}
	}
	if f&shapeTurnEffort != 0 {
		withoutTurnEffort(req)
	}
}

// knownDisplay is a thinking.display every Anthropic model has taken.
func knownDisplay(d string) bool { return d == "summarized" || d == "omitted" }

func hasTurnEffort(req map[string]any) bool {
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm != nil {
			if _, ok := mm["output_config"]; ok {
				return true
			}
		}
	}
	return false
}

// withoutTurnEffort removes the per-turn effort from req's messages; a
// system message that carried nothing else goes with it. The request's own
// output_config stays.
func withoutTurnEffort(req map[string]any) {
	msgs, _ := req["messages"].([]any)
	out := msgs[:0:0]
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm == nil {
			out = append(out, m)
			continue
		}
		if _, ok := mm["output_config"]; !ok {
			out = append(out, m)
			continue
		}
		delete(mm, "output_config")
		if mm["role"] == "system" && emptyContent(mm["content"]) {
			continue
		}
		out = append(out, mm)
	}
	req["messages"] = out
}

func emptyContent(c any) bool {
	switch v := c.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		for _, b := range v {
			bm, _ := b.(map[string]any)
			if bm == nil || bm["type"] != "text" {
				return false
			}
			if s, _ := bm["text"].(string); s != "" {
				return false
			}
		}
		return true
	}
	return false
}

// withBetweenTools is body with thinking disabled turned into between_tools
// (an effort above high asked as high); false when body does not turn
// thinking off with disabled.
func withBetweenTools(body []byte) ([]byte, bool) {
	req, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	th, _ := req["thinking"].(map[string]any)
	if th["type"] != "disabled" {
		return nil, false
	}
	req["thinking"] = map[string]any{"type": "between_tools"}
	if oc, _ := req["output_config"].(map[string]any); oc != nil {
		if e := oc["effort"]; e == "xhigh" || e == "max" {
			oc["effort"] = "high"
		}
	}
	out, err := json.Marshal(req)
	return out, err == nil
}
