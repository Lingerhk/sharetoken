package portal

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func blogServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("older/en.md", "---\ntitle: Older\ndate: 2026-01-02\ncover: cover.png\ntags: a, b\n---\n\n## Hello\n\nText ![x](pic.png)\n")
	write("older/zh-CN.md", "---\ntitle: 旧文\ndate: 2026-01-02\n---\n正文\n")
	write("older/cover.png", "\x89PNG")
	write("older/pic.png", "\x89PNG")
	write("older/notes.txt", "secret")
	write("newer/en.md", "---\ntitle: \"Newer: quoted\"\ndate: 2026-02-01\nsummary: s\n---\nbody\n")
	write("draft/en.md", "---\ntitle: Draft\ndate: 2026-03-01\ndraft: true\n---\nx\n")
	write("undated/en.md", "---\ntitle: No date\n---\nx\n")
	write("Bad_Slug/en.md", "---\ntitle: Bad\ndate: 2026-03-01\n---\nx\n")
	write("secret.png", "outside")
	return &Server{cfg: Config{BlogDir: dir}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, dir
}

func blogMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/blog", s.handleBlogList)
	mux.HandleFunc("GET /api/v1/blog/{slug}", s.handleBlogArticle)
	mux.HandleFunc("GET /blog/{slug}/{file...}", s.handleBlogAsset)
	return mux
}

func TestBlogList(t *testing.T) {
	s, _ := blogServer(t)
	rec := httptest.NewRecorder()
	blogMux(s).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/blog?lang=zh-CN", nil))
	var l BlogList
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.Total != 2 || len(l.Posts) != 2 {
		t.Fatalf("want the two published articles, got %+v", l)
	}
	if l.Posts[0].Slug != "newer" || l.Posts[0].Lang != "en" || l.Posts[0].Title != "Newer: quoted" {
		t.Errorf("newest first, falling back to English: %+v", l.Posts[0])
	}
	if p := l.Posts[1]; p.Lang != "zh-CN" || p.Title != "旧文" || strings.Join(p.Langs, ",") != "en,zh-CN" {
		t.Errorf("the reader's language when there is one: %+v", p)
	}
	rec = httptest.NewRecorder()
	blogMux(s).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/blog?limit=1", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if l.Total != 2 || len(l.Posts) != 1 || l.Posts[0].Cover != "" {
		t.Errorf("limit keeps the total: %+v", l)
	}
}

func TestBlogArticle(t *testing.T) {
	s, _ := blogServer(t)
	rec := httptest.NewRecorder()
	blogMux(s).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/blog/older?lang=en", nil))
	var a BlogArticle
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Post.Title != "Older" || a.Post.Cover != "/blog/older/cover.png" || strings.Contains(a.Markdown, "title:") || !strings.Contains(a.Markdown, "## Hello") {
		t.Errorf("article: %+v", a)
	}
	if a.Newer == nil || a.Newer.Slug != "newer" || a.Older != nil {
		t.Errorf("neighbours: %+v %+v", a.Newer, a.Older)
	}
	for _, slug := range []string{"draft", "undated", "missing", "Bad_Slug"} {
		rec = httptest.NewRecorder()
		blogMux(s).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/blog/"+slug, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", slug, rec.Code)
		}
	}
}

func TestBlogAssets(t *testing.T) {
	s, dir := blogServer(t)
	if err := os.Symlink(filepath.Join(dir, "secret.png"), filepath.Join(dir, "older", "link.png")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/blog/older/pic.png":           200,
		"/blog/older/cover.png":         200,
		"/blog/older/notes.txt":         404, // not a picture
		"/blog/older/en.md":             404, // the Markdown goes through the API
		"/blog/older/../secret.png":     404,
		"/blog/older/%2e%2e/secret.png": 404,
		"/blog/older/link.png":          404, // a link out of the folder
		"/blog/draft/x.png":             404,
		"/blog/older/":                  404,
	} {
		rec := httptest.NewRecorder()
		blogMux(s).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want && !(want == 404 && rec.Code >= 300 && rec.Code < 400) {
			t.Errorf("%s: got %d, want %d", path, rec.Code, want)
		}
	}
}

func TestBlogIndexFollowsTheFolder(t *testing.T) {
	s, dir := blogServer(t)
	if n := len(s.blogIndex()); n != 2 {
		t.Fatalf("got %d", n)
	}
	if err := os.WriteFile(filepath.Join(dir, "newer", "zh-CN.md"), []byte("---\ntitle: 新\ndate: 2026-04-01\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.blogIndex()[0].date; got != "2026-02-01" {
		t.Errorf("the index is kept between checks: %s", got)
	}
	s.blog.checked = s.blog.checked.Add(-blogCheckEvery) // the check interval passes
	if got := s.blogIndex()[0].date; got != "2026-04-01" {
		t.Errorf("a changed folder is read again: %s", got)
	}
}

func TestReadingMinutes(t *testing.T) {
	code := "```bash\n" + strings.Repeat("word ", 5000) + "\n```\n"
	if n := readingMinutes(code+strings.Repeat("word ", 460), "en"); n != 2 {
		t.Errorf("code blocks do not count: %d", n)
	}
	if n := readingMinutes(strings.Repeat("字", 1200), "zh-CN"); n != 3 {
		t.Errorf("Chinese: %d", n)
	}
}
