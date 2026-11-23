package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

func autostartCall(t *testing.T, method, body string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, autostartPath, strings.NewReader(body))
	w := httptest.NewRecorder()
	serveAutostart(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body)
	}
	return out
}

// The app's own login entry: written, reported, removed. On Windows it is a
// real value under the user's Run key (removed again at the end).
func TestAppAutostart(t *testing.T) {
	if runtime.GOOS != "windows" {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	} else if on, _ := appAutostart(); on {
		t.Skip("this user already has the app starting at login")
	}
	if out := autostartCall(t, http.MethodGet, ""); out["on"] != false || out["app"] != true {
		t.Fatalf("before: %v", out)
	}
	out := autostartCall(t, http.MethodPut, `{"on":true}`)
	if out["on"] != true {
		t.Fatalf("after on: %v", out)
	}
	where := out["where"].(string)
	if runtime.GOOS != "windows" {
		data, err := os.ReadFile(where)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), hiddenArg) {
			t.Errorf("the entry does not start the app hidden:\n%s", data)
		}
		t.Logf("%s:\n%s", where, data)
	}
	if out := autostartCall(t, http.MethodPut, `{"on":false}`); out["on"] != false {
		t.Fatalf("after off: %v", out)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(where); !os.IsNotExist(err) {
			t.Errorf("entry left: %v", err)
		}
	}
}
