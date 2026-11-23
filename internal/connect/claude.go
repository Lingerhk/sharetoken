package connect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// ClaudeSettingsPath is Claude Code's user settings file.
func ClaudeSettingsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

// pickerLabel starts the labels of the network's entries in Claude Code's
// model menu.
const pickerLabel = "ShareToken "

const keyHeaderName = "X-Sharetoken-Key"

// Claude connects Claude Code (its settings.json):
//
//   - env.ANTHROPIC_BASE_URL: the local endpoint;
//   - with Subscription: no env.ANTHROPIC_AUTH_TOKEN (Claude Code keeps its
//     claude.ai login), the API key in env.ANTHROPIC_CUSTOM_HEADERS, and the
//     network's models in modelPicker.options (Claude Code lists no
//     gateway's models while signed in);
//   - without: env.ANTHROPIC_AUTH_TOKEN is the API key, and Claude Code
//     reads the models from the endpoint
//     (CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY).
func Claude(home string, o Options) (*Applied, error) {
	path := ClaudeSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	root, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object (%v): fix it first", path, err)
	}
	st, err := LoadState(home)
	if err != nil {
		return nil, err
	}
	a := st.Claude
	if a == nil || a.Path != path {
		a = &Applied{Path: path, Created: data == nil}
	}
	if a.Original == nil {
		a.Original = map[string]*json.RawMessage{}
	}
	claudeRestore(root, a)
	env := root.child("env")
	if v := jsonString(env, "ANTHROPIC_BASE_URL"); v != "" && !localURL(v) && !o.Force {
		return nil, fmt.Errorf("%s already points Claude Code at %s: run again with -force to replace it (the old value is kept for `sharetoken connect claude -off`)", path, v)
	}
	remember := func(key string, present bool, raw json.RawMessage) {
		if _, ok := a.Original[key]; ok {
			return
		}
		if present {
			r := append(json.RawMessage(nil), raw...)
			a.Original[key] = &r
		} else {
			a.Original[key] = nil
		}
	}
	setEnv := func(k string, v any) {
		raw, ok := env.get(k)
		remember("env."+k, ok, raw)
		env.setValue(k, v)
	}
	delEnv := func(k string) {
		raw, ok := env.get(k)
		remember("env."+k, ok, raw)
		env.del(k)
	}
	setTop := func(k string, v any) {
		raw, ok := root.get(k)
		remember("top."+k, ok, raw)
		root.setValue(k, v)
	}
	rawEnv, hadEnv := root.get("env")
	remember("top.env", hadEnv, rawEnv)

	setEnv("ANTHROPIC_BASE_URL", o.BaseURL)
	if o.Subscription {
		delEnv("ANTHROPIC_AUTH_TOKEN")
		if o.Key != "" {
			cur := jsonString(env, "ANTHROPIC_CUSTOM_HEADERS")
			raw, ok := env.get("ANTHROPIC_CUSTOM_HEADERS")
			remember("env.ANTHROPIC_CUSTOM_HEADERS", ok, raw)
			lines := withoutKeyLine(cur)
			lines = append(lines, keyHeaderName+": "+o.Key)
			env.setValue("ANTHROPIC_CUSTOM_HEADERS", strings.Join(lines, "\n"))
		}
		raw, ok := root.get("modelPicker")
		remember("top.modelPicker", ok, raw)
		picker := root.child("modelPicker")
		var opts []json.RawMessage
		if v, ok := picker.get("options"); ok {
			_ = json.Unmarshal(v, &opts)
		}
		opts = withoutOurOptions(opts)
		for _, m := range o.Models {
			b, _ := json.Marshal(map[string]string{"model": "st-" + m, "label": pickerLabel + m, "description": "ShareToken network", "behavesAs": behavesAs(m)})
			opts = append(opts, b)
		}
		picker.setValue("options", opts)
		root.set("modelPicker", picker.compact())
	} else {
		key := o.Key
		if key == "" {
			key = "sharetoken"
		}
		setEnv("ANTHROPIC_AUTH_TOKEN", key)
		setEnv("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "1")
		setTop("enableArtifact", false)
	}
	root.set("env", env.compact())

	out := root.indented()
	a.Options, a.At = o, time.Now()
	a.Options.Models = nil
	if !bytes.Equal(out, data) {
		b, err := backup(home, path, "claude-settings.json")
		if err != nil {
			return nil, fmt.Errorf("backing up %s: %w", path, err)
		}
		if a.Backup == "" {
			a.Backup = b
		}
		if err := writeAtomic(path, out, 0o600); err != nil {
			return nil, err
		}
	}
	a.saveListed(o.Models)
	st.Claude = a
	return a, saveState(home, st)
}

// ClaudeOff puts Claude Code's settings back as they were.
func ClaudeOff(home string) error {
	st, err := LoadState(home)
	if err != nil || st.Claude == nil {
		return err
	}
	a := st.Claude
	data, err := os.ReadFile(a.Path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	root, err := parseObject(data)
	if err != nil {
		return fmt.Errorf("%s is not a JSON object (%v); the backup is %s", a.Path, err, a.Backup)
	}
	claudeRestore(root, a)
	out := root.indented()
	if orig, err := os.ReadFile(a.Backup); a.Backup != "" && err == nil && sameJSON(orig, out) {
		out = orig // as the user wrote it, to the byte
	}
	if a.Created && removeIfEmpty(a.Path, out) {
		st.Claude = nil
		return saveState(home, st)
	}
	if !bytes.Equal(out, data) && len(data) > 0 {
		if _, err := backup(home, a.Path, "claude-settings.json"); err != nil {
			return err
		}
		if err := writeAtomic(a.Path, out, 0o600); err != nil {
			return err
		}
	}
	st.Claude = nil
	return saveState(home, st)
}

// claudeRestore puts the managed settings back to their originals, keeping
// what the user added since: other header lines, other model menu entries.
func claudeRestore(root *object, a *Applied) {
	if len(a.Original) == 0 {
		return
	}
	env := root.child("env")
	for k, orig := range a.Original {
		switch {
		case k == "top.env":
		case k == "env.ANTHROPIC_CUSTOM_HEADERS":
			lines := withoutKeyLine(jsonString(env, "ANTHROPIC_CUSTOM_HEADERS"))
			if len(lines) == 0 && orig == nil {
				env.del("ANTHROPIC_CUSTOM_HEADERS")
			} else if len(lines) > 0 {
				env.setValue("ANTHROPIC_CUSTOM_HEADERS", strings.Join(lines, "\n"))
			} else {
				env.set("ANTHROPIC_CUSTOM_HEADERS", *orig)
			}
		case k == "top.modelPicker":
			picker := root.child("modelPicker")
			var opts []json.RawMessage
			if v, ok := picker.get("options"); ok {
				_ = json.Unmarshal(v, &opts)
			}
			opts = withoutOurOptions(opts)
			if len(opts) == 0 && len(picker.keys) <= 1 && orig == nil {
				root.del("modelPicker")
			} else {
				picker.setValue("options", opts)
				root.set("modelPicker", picker.compact())
			}
		case strings.HasPrefix(k, "env."):
			if orig == nil {
				env.del(strings.TrimPrefix(k, "env."))
			} else {
				env.set(strings.TrimPrefix(k, "env."), *orig)
			}
		case strings.HasPrefix(k, "top."):
			if orig == nil {
				root.del(strings.TrimPrefix(k, "top."))
			} else {
				root.set(strings.TrimPrefix(k, "top."), *orig)
			}
		}
	}
	if orig, ok := a.Original["top.env"]; ok && orig == nil && len(env.keys) == 0 {
		root.del("env")
	} else if _, had := root.get("env"); had || len(env.keys) > 0 {
		root.set("env", env.compact())
	}
}

// sameJSON reports whether two JSON documents hold the same value.
func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func jsonString(o *object, k string) string {
	var s string
	if v, ok := o.get(k); ok {
		_ = json.Unmarshal(v, &s)
	}
	return s
}

// behavesAs is the model Claude Code treats a network model as (its
// window, prompt profile and effort levels; modelPicker's behavesAs): the
// Claude model itself, else a Sonnet every recent Claude Code knows.
func behavesAs(model string) string {
	if strings.HasPrefix(model, "claude-") {
		return model
	}
	return "claude-sonnet-4-6"
}

// withoutKeyLine is a custom headers value's lines but the API key's.
func withoutKeyLine(v string) []string {
	var out []string
	for _, l := range strings.Split(v, "\n") {
		name, _, _ := strings.Cut(l, ":")
		if strings.TrimSpace(l) == "" || strings.EqualFold(strings.TrimSpace(name), keyHeaderName) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// withoutOurOptions drops the network's entries from a model menu.
func withoutOurOptions(opts []json.RawMessage) []json.RawMessage {
	out := []json.RawMessage{}
	for _, o := range opts {
		var e struct {
			Model string `json:"model"`
			Label string `json:"label"`
		}
		_ = json.Unmarshal(o, &e)
		if strings.HasPrefix(e.Model, "st-") && strings.HasPrefix(e.Label, pickerLabel) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// listed is the models last put in the menu (kept in Lines).
func (a *Applied) listed() []string {
	if a.Lines == nil || a.Lines["models"] == "" {
		return nil
	}
	return strings.Split(a.Lines["models"], "\n")
}

func (a *Applied) saveListed(models []string) {
	if a.Lines == nil {
		a.Lines = map[string]string{}
	}
	a.Lines["models"] = strings.Join(models, "\n")
}
