package portal

// The user guide is Markdown under web/docs/<language>/. These tests keep the
// languages in step, every link pointing somewhere, and every command real.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const docsSource = "zh-CN"

var docsLangs = []string{"en", "zh-CN"}

type docsPage struct {
	Slug  string            `json:"slug"`
	Title map[string]string `json:"title"`
}

func docsNav(t *testing.T) []docsPage {
	t.Helper()
	raw, err := os.ReadFile("web/docs-nav.json")
	if err != nil {
		t.Fatal(err)
	}
	var groups []struct {
		Title map[string]string `json:"title"`
		Pages []docsPage        `json:"pages"`
	}
	if err := json.Unmarshal(raw, &groups); err != nil {
		t.Fatal(err)
	}
	var pages []docsPage
	for _, g := range groups {
		for _, lang := range docsLangs {
			if g.Title[lang] == "" {
				t.Errorf("a navigation group has no %s title", lang)
			}
		}
		pages = append(pages, g.Pages...)
	}
	return pages
}

func readDoc(t *testing.T, lang, slug string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("web/docs", lang, slug+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// docOutline is the page with its prose removed: what must match in every
// language, in order.
func docOutline(src string) (outline, codes []string) {
	fence := false
	var code []string
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasPrefix(line, "```"):
			if fence {
				codes = append(codes, strings.Join(code, "\n"))
				code = nil
			}
			fence = !fence
			outline = append(outline, line)
		case fence:
			code = append(code, line)
		case strings.HasPrefix(line, ":::"), strings.HasPrefix(line, "> [!"):
			outline = append(outline, line)
		case strings.HasPrefix(line, "#"):
			head, _ := regexp.MatchString(`\{#[a-z0-9-]+\}$`, line)
			level := strings.SplitN(line, " ", 2)[0]
			if head {
				outline = append(outline, level+" "+line[strings.LastIndex(line, "{#"):])
			} else {
				outline = append(outline, level)
			}
		case strings.HasPrefix(line, "|"):
			outline = append(outline, fmt.Sprintf("table row of %d", strings.Count(strings.ReplaceAll(line, `\|`, ""), "|")))
		case strings.HasPrefix(line, "- "):
			outline = append(outline, "-")
		default:
			if m := regexp.MustCompile(`^\d+\. `).FindString(line); m != "" {
				outline = append(outline, m)
			}
		}
	}
	return outline, codes
}

var (
	docLinkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	docIDRe   = regexp.MustCompile(`\{#([a-z0-9-]+)\}\s*$`)
	docVarRe  = regexp.MustCompile(`\{\{(\w*)\}\}`)
	docKnown  = map[string]bool{"exe": true, "home": true, "homewin": true, "port": true, "version": true, "origin": true, "support": true}
)

func TestDocsPagesMatchAcrossLanguages(t *testing.T) {
	pages := docsNav(t)
	if len(pages) == 0 {
		t.Fatal("no pages")
	}
	listed := map[string]bool{}
	for _, p := range pages {
		if listed[p.Slug] {
			t.Errorf("%s listed twice", p.Slug)
		}
		listed[p.Slug] = true
		srcOutline, srcCode := docOutline(readDoc(t, docsSource, p.Slug))
		for _, lang := range docsLangs {
			src := readDoc(t, lang, p.Slug)
			if want := "# " + p.Title[lang] + "\n"; !strings.HasPrefix(src, want) {
				t.Errorf("%s/%s must start with %q", lang, p.Slug, want)
			}
			if lang == docsSource {
				continue
			}
			outline, code := docOutline(src)
			if a, b := strings.Join(srcOutline, "\n"), strings.Join(outline, "\n"); a != b {
				t.Errorf("%s/%s has another structure than %s:\n%s", lang, p.Slug, docsSource, firstDiff(a, b))
			}
			if len(code) != len(srcCode) {
				t.Errorf("%s/%s: %d code blocks, %s has %d", lang, p.Slug, len(code), docsSource, len(srcCode))
				continue
			}
			for i := range code {
				if code[i] != srcCode[i] {
					t.Errorf("%s/%s: code block %d differs from %s:\n%s", lang, p.Slug, i, docsSource, firstDiff(srcCode[i], code[i]))
				}
			}
			// The same places are linked, in the same order.
			if a, b := fmt.Sprint(docLinkRe.FindAllStringSubmatch(readDoc(t, docsSource, p.Slug), -1)), fmt.Sprint(docLinkRe.FindAllStringSubmatch(src, -1)); a != b {
				t.Errorf("%s/%s links differ from %s:\n%s", lang, p.Slug, docsSource, firstDiff(a, b))
			}
		}
	}
	// Nothing on disk that the navigation does not reach.
	for _, lang := range docsLangs {
		files, _ := filepath.Glob(filepath.Join("web/docs", lang, "*.md"))
		for _, f := range files {
			if slug := strings.TrimSuffix(filepath.Base(f), ".md"); !listed[slug] {
				t.Errorf("%s is not in docs-nav.json", f)
			}
		}
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("  line %d: %q\n        vs %q", i+1, x, y)
		}
	}
	return ""
}

// Every internal link goes to a page that exists and, when it names one, to a heading on it.
func TestDocsLinksResolve(t *testing.T) {
	pages := docsNav(t)
	app := map[string]bool{"": true, "#register": true, "#/devices": true, "#/ledger": true, "#/jobs": true, "#/tokens": true, "#/account": true, "#/feedback": true, "#/pairing": true}
	for _, lang := range docsLangs {
		ids := map[string]map[string]bool{}
		for _, p := range pages {
			set := map[string]bool{}
			for _, line := range strings.Split(readDoc(t, lang, p.Slug), "\n") {
				if m := docIDRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "#") {
					if set[m[1]] {
						t.Errorf("%s/%s: duplicate heading id %q", lang, p.Slug, m[1])
					}
					set[m[1]] = true
				}
			}
			ids[p.Slug] = set
		}
		for _, p := range pages {
			src := readDoc(t, lang, p.Slug)
			for _, line := range strings.Split(src, "\n") {
				if strings.HasPrefix(line, "## ") && !docIDRe.MatchString(line) || strings.HasPrefix(line, "### ") && !docIDRe.MatchString(line) {
					t.Errorf("%s/%s: heading without an {#id}: %s", lang, p.Slug, line)
				}
			}
			for _, m := range docLinkRe.FindAllStringSubmatch(src, -1) {
				link := m[1]
				switch {
				case strings.HasPrefix(link, "https://"):
				case strings.HasPrefix(link, "#"):
					if !ids[p.Slug][link[1:]] {
						t.Errorf("%s/%s: no heading %s", lang, p.Slug, link)
					}
				case strings.HasPrefix(link, "/docs/"):
					slug, frag, _ := strings.Cut(strings.TrimPrefix(link, "/docs/"), "#")
					if set, ok := ids[slug]; !ok || (frag != "" && !set[frag]) {
						t.Errorf("%s/%s: broken link %s", lang, p.Slug, link)
					}
				case strings.HasPrefix(link, "/app/"):
					if !app[strings.TrimPrefix(link, "/app/")] {
						t.Errorf("%s/%s: unknown portal page %s", lang, p.Slug, link)
					}
				case link == "/", link == "/#download":
				default:
					t.Errorf("%s/%s: unexpected link %s", lang, p.Slug, link)
				}
			}
			for _, m := range docVarRe.FindAllStringSubmatch(src, -1) {
				if !docKnown[m[1]] {
					t.Errorf("%s/%s: unknown placeholder %s", lang, p.Slug, m[0])
				}
			}
			if strings.Count(src, "```")%2 != 0 {
				t.Errorf("%s/%s: unclosed code block", lang, p.Slug)
			}
			opens, closes := 0, 0
			for _, line := range strings.Split(src, "\n") {
				if strings.HasPrefix(line, "::: ") {
					opens++
				} else if strings.TrimSpace(line) == ":::" {
					closes++
				}
			}
			if opens != closes {
				t.Errorf("%s/%s: %d ::: blocks opened, %d closed", lang, p.Slug, opens, closes)
			}
		}
	}
}

// Every command and flag the guide shows exists in the client, and every
// command of the client is documented.
func TestDocsCommandsExist(t *testing.T) {
	raw, err := os.ReadFile("../../cmd/sharetoken/main.go")
	if err != nil {
		t.Fatal(err)
	}
	main := string(raw)
	commands := map[string]bool{}
	for _, m := range regexp.MustCompile(`case ((?:"[a-z]+"(?:, )?)+):`).FindAllStringSubmatch(main[strings.Index(main, "switch cmd"):strings.Index(main, "type cmdCtx")], -1) {
		for _, c := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(m[1], -1) {
			commands[c[1]] = true
		}
	}
	commands["version"] = true
	if len(commands) < 12 {
		t.Fatalf("parsed only %v", commands)
	}
	shareSubs := map[string]bool{"detect": true, "add": true, "rm": true, "on": true, "off": true, "upstream": true}
	flags := map[string]bool{"home": true, "header": true}
	sources, _ := filepath.Glob("../../cmd/sharetoken/*.go") // commands may keep their flags in files of their own
	for _, f := range sources {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`fs\.(?:String|Bool|Int|Int64|Uint64|Duration)\("([a-z-]+)"`).FindAllStringSubmatch(string(src), -1) {
			flags[m[1]] = true
		}
	}

	invocation := regexp.MustCompile(`\{\{exe\}\}((?: +-home +(?:"[^"]*"|\S+))?) +([a-z]+)((?: +[^|&;>` + "`" + `]*)?)`)
	flagRe := regexp.MustCompile(`(?:^|\s)-([a-z][a-z-]*)`)
	seen := map[string]bool{}
	for _, p := range docsNav(t) {
		src := readDoc(t, docsSource, p.Slug)
		for _, m := range invocation.FindAllStringSubmatch(src, -1) {
			cmd, rest := m[2], m[3]
			seen[cmd] = true
			if !commands[cmd] {
				t.Errorf("%s: %q is not a command: %s", p.Slug, cmd, m[0])
			}
			if cmd == "share" {
				if sub := strings.Fields(rest); len(sub) > 0 && !shareSubs[sub[0]] {
					t.Errorf("%s: %q is not a share command: %s", p.Slug, sub[0], m[0])
				}
			}
			for _, f := range flagRe.FindAllStringSubmatch(rest, -1) {
				if !flags[f[1]] {
					t.Errorf("%s: flag -%s does not exist: %s", p.Slug, f[1], m[0])
				}
			}
		}
	}
	for cmd := range commands {
		switch cmd {
		case "run", "account": // aliases, named in the reference
			continue
		}
		if !seen[cmd] {
			if !strings.Contains(readDoc(t, docsSource, "cli"), "### "+cmd+" ") {
				t.Errorf("command %q is not documented", cmd)
			}
		}
	}
}

// The filenames the guide builds must be the ones the build script produces.
func TestDocsPackageNames(t *testing.T) {
	src := readDoc(t, docsSource, "quickstart")
	for _, want := range []string{
		"sharetoken-cli-{{version}}-macos-$ARCH.tar.gz",
		"sharetoken-cli-{{version}}-linux-$ARCH.tar.gz",
		"sharetoken-cli-{{version}}-windows-$arch",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("quickstart does not use %s", want)
		}
	}
	for _, name := range []string{"sharetoken-cli-1.0.1-macos-arm64.tar.gz", "sharetoken-cli-dev-1.0.1-dev.abc1234.dirty-linux-amd64.tar.gz", "sharetoken-cli-1.0.1-windows-amd64.zip", "sharetoken-1.0.1-macos-arm64.dmg", "sharetoken-1.0.1-windows-amd64.exe", "sharetoken-1.0.1-linux-arm64.deb"} {
		if !packageRe.MatchString(name) {
			t.Errorf("%s is not recognised as a package", name)
		}
	}
	raw, err := os.ReadFile("../../scripts/build/build-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "base=sharetoken-cli$SUFFIX-$VERSION-$os-$arch") {
		t.Error("scripts/build/build-cli.sh names its packages differently now: update the guide and packageRe")
	}
}
