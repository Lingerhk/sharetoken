package portal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// i18nBundles maps each dictionary to the files its source strings live in.
var i18nBundles = map[string]struct {
	dir   string // holds i18n/<bundle>/<lang>.json
	files []string
}{
	"site":   {"web", []string{"web/index.html", "web/site.js", "web/docs.html", "web/docs.js"}},
	"app":    {"web", []string{"web/app.html", "web/common.js", "web/user.js"}},
	"client": {"../client/ui", []string{"../client/ui/index.html", "../client/ui/app.js"}},
}

var (
	jsKeyRe     = regexp.MustCompile(`\b(?:tr|parts|mark)\(\s*'((?:[^'\\\n])*)'`)
	richRe      = regexp.MustCompile(`<(\w+)[^>]*\bdata-i18n-rich\b[^>]*>`)
	htmlTextRe  = regexp.MustCompile(`>([^<>]*\p{Han}[^<>]*)<`)
	htmlAttrRe  = regexp.MustCompile(`(?:aria-label|title|alt|placeholder|content)="([^"]*\p{Han}[^"]*)"`)
	commentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	jsCommentRe = regexp.MustCompile(`(?m)^\s*//.*$`)
	hanRe       = regexp.MustCompile(`\p{Han}`)
	varRe       = regexp.MustCompile(`\{\w+\}`)
	tagRe       = regexp.MustCompile(`</?(?:br|em|strong)>`)
	langRe      = regexp.MustCompile(`'?([A-Za-z-]+)'?: '[^']+'`)
)

// i18nKeys returns the source strings of a bundle. The JS check also fails on
// Chinese text that does not go through tr(): it would never be translated.
func i18nKeys(t *testing.T, bundle string) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, f := range i18nBundles[bundle].files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if strings.HasSuffix(f, ".js") {
			for _, m := range jsKeyRe.FindAllStringSubmatch(src, -1) {
				keys[m[1]] = true
			}
			rest := jsCommentRe.ReplaceAllString(jsKeyRe.ReplaceAllString(src, ""), "")
			for i, line := range strings.Split(rest, "\n") {
				if hanRe.MatchString(line) {
					t.Errorf("%s: Chinese text outside tr() near line %d: %.100s", f, i+1, strings.TrimSpace(line))
				}
			}
			continue
		}
		src = commentRe.ReplaceAllString(src, "")
		// A rich element's key is its whole inner markup, up to its own closing tag.
		for {
			loc := richRe.FindStringSubmatchIndex(src)
			if loc == nil {
				break
			}
			closing := "</" + src[loc[2]:loc[3]] + ">"
			end := strings.Index(src[loc[1]:], closing)
			if end < 0 {
				t.Fatalf("%s: unclosed data-i18n-rich element", f)
			}
			keys[strings.TrimSpace(src[loc[1]:loc[1]+end])] = true
			src = src[:loc[0]] + src[loc[1]+end+len(closing):]
		}
		for _, re := range []*regexp.Regexp{htmlTextRe, htmlAttrRe} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				keys[strings.TrimSpace(m[1])] = true
			}
		}
	}
	return keys
}

func i18nLangs(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("web/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	block := src[strings.Index(src, "const LANGS = {"):]
	block = block[:strings.Index(block, "};")]
	var langs []string
	for _, m := range langRe.FindAllStringSubmatch(block, -1) {
		langs = append(langs, m[1])
	}
	if len(langs) != 20 {
		t.Fatalf("want 20 languages, parsed %d: %v", len(langs), langs)
	}
	return langs
}

func sorted(ss []string) string { sort.Strings(ss); return strings.Join(ss, " ") }

// With I18N_DUMP=<dir> the test also writes each bundle's source strings there.
func TestI18nDictionaries(t *testing.T) {
	for bundle, b := range i18nBundles {
		keys := i18nKeys(t, bundle)
		if len(keys) == 0 {
			t.Fatalf("%s: no strings extracted", bundle)
		}
		if dir := os.Getenv("I18N_DUMP"); dir != "" {
			list := make([]string, 0, len(keys))
			for k := range keys {
				list = append(list, k)
			}
			sort.Strings(list)
			out, _ := json.MarshalIndent(list, "", " ")
			if err := os.WriteFile(filepath.Join(dir, bundle+".json"), out, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, lang := range i18nLangs(t) {
			if lang == "zh-CN" { // the source language has no dictionary
				continue
			}
			path := filepath.Join(b.dir, "i18n", bundle, lang+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Error(err)
				continue
			}
			var dict map[string]string
			if err := json.Unmarshal(raw, &dict); err != nil {
				t.Errorf("%s: %v", path, err)
				continue
			}
			for k := range keys {
				if _, ok := dict[k]; !ok {
					t.Errorf("%s: missing %q", path, k)
				}
			}
			for k, v := range dict {
				if !keys[k] {
					t.Errorf("%s: stale key %q", path, k)
				}
				if strings.TrimSpace(v) == "" {
					t.Errorf("%s: empty translation of %q", path, k)
				}
				if a, b := sorted(varRe.FindAllString(k, -1)), sorted(varRe.FindAllString(v, -1)); a != b {
					t.Errorf("%s: placeholders of %q: want [%s], got [%s]", path, k, a, b)
				}
				if a, b := sorted(tagRe.FindAllString(k, -1)), sorted(tagRe.FindAllString(v, -1)); a != b {
					t.Errorf("%s: markup of %q: want [%s], got [%s]", path, k, a, b)
				}
				if a, b := strings.Count(k, "|"), strings.Count(v, "|"); a != b {
					t.Errorf("%s: %q needs %d separators, has %d", path, k, a, b)
				}
				if lang != "ja" && lang != "zh-TW" && hanRe.MatchString(v) {
					t.Errorf("%s: Chinese left in the translation of %q: %q", path, k, v)
				}
			}
		}
	}
}

func TestI18nRuntimeSharedByPortalAndClient(t *testing.T) {
	a, err := os.ReadFile("web/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../client/ui/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("portal/web/i18n.js and client/ui/i18n.js differ")
	}
}

// The admin console stays Chinese: it pins the source language.
func TestAdminPageIsNotTranslated(t *testing.T) {
	raw, err := os.ReadFile("web/admin.html")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`data-lang=%q`, "zh-CN"); !strings.Contains(string(raw), want) {
		t.Errorf("admin.html must load i18n.js with %s", want)
	}
}
