package apicompat

import (
	"strings"
	"testing"
)

func respItemsOf(t *testing.T, body string) []respItem {
	t.Helper()
	r, err := respParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r.items
}

func respDescribe(items []respItem) string {
	var out []string
	for _, it := range items {
		switch it.kind {
		case respItemMessage:
			out = append(out, it.role+":"+respJoinText(it.parts))
		case respItemReasoning:
			out = append(out, "reasoning:"+it.summary)
		case respItemCall:
			out = append(out, "call:"+it.callID+":"+it.name+":"+it.args)
		case respItemOutput:
			out = append(out, "output:"+it.callID+":"+it.outText)
		}
	}
	return strings.Join(out, " | ")
}

func TestResponsesLowerItems(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"string input",
			`{"input":"hi"}`, `user:hi`},
		{"strings and bare parts in the input array",
			`{"input":["a",{"type":"input_text","text":"b"},{"type":"input_image","image_url":"https://x/i.png"}]}`,
			`user:a | user:b | user:`},
		{"roles",
			`{"input":[{"role":"system","content":"s"},{"role":"developer","content":"d"},{"role":"tool","content":"t"},{"role":"assistant","content":[{"type":"output_text","text":"a"}]}]}`,
			`system:s | system:d | user:t | assistant:a`},
		{"text field instead of content",
			`{"input":[{"type":"message","role":"user","text":"via text"}]}`, `user:via text`},
		{"agent message",
			`{"input":[{"type":"agent_message","content":[{"type":"input_text","text":"task: "},{"type":"encrypted_content","encrypted_content":"do X"}]},{"type":"agent_message","content":[]}]}`,
			`user:task: do X`},
		{"reasoning text from content when the summary is empty",
			`{"input":[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"raw"}]},{"type":"message","role":"assistant","content":"x"}]}`,
			`reasoning:raw | assistant:x`},
		{"call id from id, empty arguments, string tool_search arguments",
			`{"input":[{"type":"function_call","id":"fc_1","name":"f","arguments":""},{"type":"tool_search_call","call_id":"s","arguments":"{\"query\":\"q\"}"},{"type":"tool_search_call","call_id":"s2","arguments":"bad"},
			{"type":"function_call_output","call_id":"fc_1","output":"o"},{"type":"tool_search_output","call_id":"s","output":{"x":1}},{"type":"tool_search_output","call_id":"s2","tools":[]}]}`,
			`call:fc_1:f:{} | call:s:tool_search:{"query":"q"} | call:s2:tool_search:{} | output:fc_1:o | output:s:{"x":1} | output:s2:[]`},
		{"outputs without call id dropped",
			`{"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","output":"x"},{"type":"function_call_output","call_id":"c","output":null}]}`,
			`call:c:f:{} | output:c:`},
		{"repeated call id: one call, the last output",
			`{"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"1"},{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"2"}]}`,
			`call:c:f:{} | output:c:2`},
		{"undeclared namespace call keeps a flattened name",
			`{"input":[{"type":"function_call","call_id":"c","namespace":"ns","name":"f.g","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"x"}]}`,
			`call:c:` + respSafeToolName("ns__f.g") + `:{} | output:c:x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := respDescribe(respItemsOf(t, tt.body)); got != tt.want {
				t.Errorf("\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestResponsesInstructionsArray(t *testing.T) {
	r, err := respParseRequest([]byte(`{"instructions":[{"type":"message","role":"system","content":[{"type":"input_text","text":"one"}]},{"role":"system","content":"two"}],"input":"x"}`))
	if err != nil || r.instructions != "one\n\ntwo" {
		t.Errorf("%v %q", err, r.instructions)
	}
	for _, bad := range []string{`{"input":{"x":1}}`, `{"input":"x","tools":[{"type":"custom","name":"a"},{"type":"function","name":"a"}]}`,
		`{"input":[{"type":"additional_tools","tools":"nope"}]}`} {
		if _, err := respParseRequest([]byte(bad)); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestResponsesParseOutput(t *testing.T) {
	tests := []struct {
		name, raw, text string
		images          []string
	}{
		{"string", `"plain"`, "plain", nil},
		{"null", `null`, "", nil},
		{"object", `{"a":1}`, `{"a":1}`, nil},
		{"text parts", `[{"type":"input_text","text":"a"},{"type":"output_text","text":"b"}]`, "a\nb", nil},
		{"image data URL string", `"data:image/png;base64,QQ=="`, respMediaMarker, []string{"data:image/png;base64,QQ=="}},
		{"not an image data URL", `"data:text/plain;base64,QQ=="`, "data:text/plain;base64,QQ==", nil},
		{"parts with an image", `[{"type":"input_text","text":"a"},{"type":"input_image","image_url":{"url":"https://x/i.png"}}]`,
			"a\n" + respMediaMarker, []string{"https://x/i.png"}},
		{"mixed array is JSON", `[{"type":"input_text","text":"a"},{"type":"other"}]`, `[{"type":"input_text","text":"a"},{"type":"other"}]`, nil},
		{"nested image in a JSON string", `"{\"content\":[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,Qg==\"}],\"ok\":true}"`,
			`{"content":[{"text":"` + respMediaMarker + `","type":"input_text"}],"ok":true}`, []string{"data:image/png;base64,Qg=="}},
		{"MCP image object", `{"content":[{"type":"image","data":"Qw==","mimeType":"image/webp"}]}`,
			`{"content":[{"text":"` + respMediaMarker + `","type":"text"}]}`, []string{"data:image/webp;base64,Qw=="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, parts, media := respParseOutput([]byte(tt.raw))
			if text != tt.text {
				t.Errorf("text %q, want %q", text, tt.text)
			}
			var images []string
			for _, p := range parts {
				if p.kind == "image" {
					images = append(images, p.url)
				}
			}
			if strings.Join(images, ",") != strings.Join(tt.images, ",") || media != (len(tt.images) > 0) {
				t.Errorf("images %v (media %v), want %v", images, media, tt.images)
			}
		})
	}
}

func TestResponsesDataURL(t *testing.T) {
	tests := []struct {
		in, mt, data string
		ok           bool
	}{
		{"data:image/png;base64,QQ==", "image/png", "QQ==", true},
		{"data:text/plain,hello", "text/plain", "aGVsbG8=", true},
		{"data:;base64,QQ==", "", "QQ==", false},
		{"https://x", "", "", false},
		{"data:image/png;base64", "", "", false},
	}
	for _, tt := range tests {
		mt, data, ok := respDataURL(tt.in)
		if mt != tt.mt || data != tt.data || ok != tt.ok {
			t.Errorf("respDataURL(%q) = %q %q %v", tt.in, mt, data, ok)
		}
	}
}

func TestResponsesToMessagesTextDocument(t *testing.T) {
	m := respBody(t, respConvert(t, Messages, `{"model":"m","input":[{"role":"user","content":[
		{"type":"input_file","filename":"n.txt","file_data":"data:text/plain;base64,aGk="},
		{"type":"input_file","file_url":"https://x/doc.pdf"},
		{"type":"input_file","file_data":"data:application/zip;base64,UEs="}]}]}`, Options{}))
	got := respJSON(t, respBlocks(respMsgs(t, m)[0]))
	want := `[{"source":{"data":"hi","media_type":"text/plain","type":"text"},"title":"n.txt","type":"document"},{"cache_control":{"type":"ephemeral"},"source":{"type":"url","url":"https://x/doc.pdf"},"type":"document"}]`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestResponsesToMessagesToolUseIDs(t *testing.T) {
	m := respBody(t, respConvert(t, Messages, `{"model":"m","input":[{"role":"user","content":"x"},
		{"type":"function_call","call_id":"call.1/a","name":"f","arguments":"[1]"},{"type":"function_call_output","call_id":"call.1/a","output":"o"}]}`, Options{}))
	msgs := respMsgs(t, m)
	respCheckMessagesSendable(t, msgs)
	use := respBlocks(msgs[1])[0]
	if use["id"] != "call_1_a" || respJSON(t, use["input"]) != `{}` {
		t.Errorf("tool_use %v", use)
	}
}
