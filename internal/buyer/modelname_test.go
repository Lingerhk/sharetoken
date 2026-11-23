package buyer

import "testing"

func TestNetworkModel(t *testing.T) {
	for in, want := range map[string]string{
		"st-gpt-5.6-sol":         "gpt-5.6-sol",
		"st-claude-opus-5-5[1m]": "claude-opus-5-5",
		"claude-opus-5-5":        "claude-opus-5-5",
		" st-x ":                 "x",
	} {
		if got := NetworkModel(in); got != want {
			t.Errorf("NetworkModel(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsListedModel("st-x") || IsListedModel("claude-opus-5-5") {
		t.Error("IsListedModel")
	}
}
