package apicompat

import "encoding/json"

// ObjectRootSchema is a tool's parameters with an object at the root and
// no oneOf, anyOf or allOf there, folded as for Anthropic's input_schema
// (chatAnthropicSchema): other upstreams that take full JSON Schema still
// want an object root, Gemini's parametersJsonSchema among them.
func ObjectRootSchema(schema json.RawMessage) json.RawMessage {
	return chatAnthropicSchema(schema)
}
