package portal

// The blog at /blog/: best-practice articles kept outside the repository. A
// directory on the server (Config.BlogDir) holds one folder per article:
//
//	<dir>/<slug>/en.md        the article in English
//	<dir>/<slug>/zh-CN.md     the article in Simplified Chinese
//	<dir>/<slug>/*.png …      its pictures, linked from the Markdown by relative path
//
// Each Markdown file starts with a front matter block:
//
//	---
//	title: How to …
//	date: 2026-11-24
//	summary: one or two sentences for the cards
//	cover: cover.png          (optional, a picture in the same folder)
//	tags: partners, codex     (optional)
//	author: ShareToken team   (optional)
//	draft: true               (optional: not listed, not served)
//	---
//
// The portal keeps only the index in memory (which articles exist, their
// titles, dates, summaries, covers, languages, reading time). It is rebuilt
// when the folder changes, checked at most every blogCheckEvery; article
// bodies and pictures are read from disk per request. The pages render the
// Markdown in the browser, as the guide does (blog.js).

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	blogCheckEvery = 30 * time.Second // how stale the index may get before the folder is looked at again
	blogMaxArticle = 512 << 10        // bytes of Markdown per article and language
	blogMaxPosts   = 500
)

var (
	blogSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
	blogDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	// What readingMinutes leaves out.
	blogCodeRe  = regexp.MustCompile("(?ms)^\\s*```.*?^\\s*```")
	blogImageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	blogLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// The languages articles are written in; a reader of another one gets English,
	// a reader of Traditional Chinese the Simplified article.
	blogLangs = []string{"en", "zh-CN"}
	// Files an article folder may serve besides its Markdown.
	blogAssetTypes = map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
		".webp": "image/webp", ".avif": "image/avif",
	}
)

// BlogPost is one article in one language, as the index knows it.
type BlogPost struct {
	Slug    string   `json:"slug"`
	Lang    string   `json:"lang"`  // the language of the fields below
	Langs   []string `json:"langs"` // every language the article is written in
	Title   string   `json:"title"`
	Summary string   `json:"summary,omitempty"`
	Date    string   `json:"date"`            // YYYY-MM-DD
	Cover   string   `json:"cover,omitempty"` // URL of the cover picture
	Tags    []string `json:"tags,omitempty"`
	Author  string   `json:"author,omitempty"`
	Minutes int      `json:"minutes"` // reading time
}

// BlogList is the answer of GET /api/v1/blog.
type BlogList struct {
	Total int        `json:"total"`
	Posts []BlogPost `json:"posts"` // newest first
}

// BlogArticle is the answer of GET /api/v1/blog/{slug}.
type BlogArticle struct {
	Post     BlogPost  `json:"post"`
	Markdown string    `json:"markdown"` // the body, without its front matter
	Newer    *BlogPost `json:"newer,omitempty"`
	Older    *BlogPost `json:"older,omitempty"`
}

type blogEntry struct {
	slug  string
	date  string
	langs map[string]BlogPost
}

// blogCache is the in-memory index.
type blogCache struct {
	mu      sync.Mutex
	checked time.Time // when the folder was last looked at
	sig     string    // what it looked like then
	posts   []blogEntry
}

func blogLang(asked string) string {
	switch strings.ToLower(asked) {
	case "zh-cn", "zh-tw", "zh", "zh-hans", "zh-hant":
		return "zh-CN"
	}
	return "en"
}

func (e blogEntry) in(lang string) BlogPost {
	if p, ok := e.langs[lang]; ok {
		return p
	}
	if p, ok := e.langs["en"]; ok {
		return p
	}
	return e.langs["zh-CN"]
}

// blogIndex returns the index, rebuilt first when the folder changed.
func (s *Server) blogIndex() []blogEntry {
	if s.cfg.BlogDir == "" {
		return nil
	}
	c := &s.blog
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if !c.checked.IsZero() && now.Sub(c.checked) < blogCheckEvery {
		return c.posts
	}
	c.checked = now
	sig := s.blogSignature()
	if sig == c.sig && c.posts != nil {
		return c.posts
	}
	c.posts, c.sig = s.readBlog(), sig
	s.log.Info("blog index", "posts", len(c.posts))
	return c.posts
}

// blogSignature names every Markdown file with its size and time, so a new,
// removed or edited article shows as a change. Pictures do not matter here.
func (s *Server) blogSignature() string {
	var b strings.Builder
	dirs, err := os.ReadDir(s.cfg.BlogDir)
	if err != nil {
		return "error:" + err.Error()
	}
	for _, d := range dirs {
		if !d.IsDir() || !blogSlugRe.MatchString(d.Name()) {
			continue
		}
		for _, lang := range blogLangs {
			if fi, err := os.Stat(path.Join(s.cfg.BlogDir, d.Name(), lang+".md")); err == nil {
				b.WriteString(d.Name() + "/" + lang + ":" + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "\n")
			}
		}
	}
	return b.String()
}

func (s *Server) readBlog() []blogEntry {
	out := []blogEntry{}
	dirs, err := os.ReadDir(s.cfg.BlogDir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.Warn("blog directory", "err", err)
		}
		return out
	}
	for _, d := range dirs {
		if !d.IsDir() || !blogSlugRe.MatchString(d.Name()) || len(out) >= blogMaxPosts {
			continue
		}
		e := blogEntry{slug: d.Name(), langs: map[string]BlogPost{}}
		for _, lang := range blogLangs {
			raw, err := s.readBlogFile(d.Name(), lang)
			if err != nil {
				continue
			}
			meta, body := splitFrontMatter(raw)
			if meta["draft"] == "true" || meta["title"] == "" || !blogDateRe.MatchString(meta["date"]) {
				if meta["draft"] != "true" {
					s.log.Warn("blog article needs a title and a date (YYYY-MM-DD)", "article", d.Name(), "lang", lang)
				}
				continue
			}
			p := BlogPost{Slug: d.Name(), Lang: lang, Title: meta["title"], Summary: meta["summary"], Date: meta["date"],
				Author: meta["author"], Minutes: readingMinutes(body, lang)}
			if c := meta["cover"]; c != "" && validAsset(c) {
				p.Cover = "/blog/" + d.Name() + "/" + c
			}
			for _, t := range strings.Split(meta["tags"], ",") {
				if t = strings.TrimSpace(t); t != "" {
					p.Tags = append(p.Tags, t)
				}
			}
			e.langs[lang] = p
			if p.Date > e.date {
				e.date = p.Date
			}
		}
		if len(e.langs) == 0 {
			continue
		}
		var langs []string
		for _, lang := range blogLangs {
			if _, ok := e.langs[lang]; ok {
				langs = append(langs, lang)
			}
		}
		for lang, p := range e.langs {
			p.Langs = langs
			e.langs[lang] = p
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].date != out[j].date {
			return out[i].date > out[j].date
		}
		return out[i].slug < out[j].slug
	})
	return out
}

func (s *Server) readBlogFile(slug, lang string) ([]byte, error) {
	f, err := os.Open(path.Join(s.cfg.BlogDir, slug, lang+".md"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, blogMaxArticle+1)); err != nil {
		return nil, err
	}
	if buf.Len() > blogMaxArticle {
		return nil, errors.New("article too long")
	}
	return buf.Bytes(), nil
}

// splitFrontMatter reads "key: value" lines between the leading "---" lines.
func splitFrontMatter(raw []byte) (map[string]string, string) {
	meta := map[string]string{}
	text := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(text, "---\n") {
		return meta, text
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return meta, text
	}
	sc := bufio.NewScanner(strings.NewReader(text[4 : 4+end]))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		v = strings.Trim(v, "[]")
		meta[strings.ToLower(strings.TrimSpace(k))] = v
	}
	body := text[4+end+4:]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return meta, body
}

// readingMinutes: about 230 English words or 400 Chinese characters a minute,
// not counting code blocks, pictures and link targets, which are skimmed.
func readingMinutes(body, lang string) int {
	body = blogCodeRe.ReplaceAllString(body, "")
	body = blogImageRe.ReplaceAllString(body, "")
	body = blogLinkRe.ReplaceAllString(body, "$1")
	var n float64
	if lang == "zh-CN" {
		n = float64(utf8.RuneCountInString(body)) / 400
	} else {
		n = float64(len(strings.Fields(body))) / 230
	}
	if n < 1 {
		return 1
	}
	return int(n + .5)
}

// validAsset: a relative path to a picture inside the article's folder.
func validAsset(p string) bool {
	if !fs.ValidPath(p) || strings.HasPrefix(path.Base(p), ".") {
		return false
	}
	_, ok := blogAssetTypes[strings.ToLower(path.Ext(p))]
	return ok
}

func (s *Server) blogFind(slug string) (int, []blogEntry) {
	posts := s.blogIndex()
	for i, e := range posts {
		if e.slug == slug {
			return i, posts
		}
	}
	return -1, posts
}

// ---- HTTP ----

func (s *Server) handleBlogList(w http.ResponseWriter, r *http.Request) {
	lang := blogLang(r.URL.Query().Get("lang"))
	posts := s.blogIndex()
	out := BlogList{Total: len(posts), Posts: make([]BlogPost, 0, len(posts))}
	limit := len(posts)
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	for _, e := range posts[:limit] {
		out.Posts = append(out.Posts, e.in(lang))
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleBlogArticle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	i, posts := s.blogFind(slug)
	if i < 0 {
		writeErr(w, http.StatusNotFound, "not_found", "no such article")
		return
	}
	post := posts[i].in(blogLang(r.URL.Query().Get("lang")))
	raw, err := s.readBlogFile(slug, post.Lang)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such article")
		return
	}
	_, body := splitFrontMatter(raw)
	out := BlogArticle{Post: post, Markdown: body}
	if i > 0 {
		p := posts[i-1].in(post.Lang)
		out.Newer = &p
	}
	if i+1 < len(posts) {
		p := posts[i+1].in(post.Lang)
		out.Older = &p
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, out)
}

// serveBlogPages adds /blog/, /blog/<slug> and the articles' pictures.
func (s *Server) serveBlogPages(mux *http.ServeMux, sub fs.FS) {
	page := func(w http.ResponseWriter, r *http.Request, status int) {
		data, err := fs.ReadFile(sub, "blog.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
	mux.HandleFunc("GET /blog", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/blog/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /blog/{$}", func(w http.ResponseWriter, r *http.Request) { page(w, r, http.StatusOK) })
	mux.HandleFunc("GET /blog/{slug}", func(w http.ResponseWriter, r *http.Request) {
		if i, _ := s.blogFind(r.PathValue("slug")); i < 0 {
			page(w, r, http.StatusNotFound)
			return
		}
		page(w, r, http.StatusOK)
	})
	mux.HandleFunc("GET /blog/{slug}/{file...}", s.handleBlogAsset)
	mux.HandleFunc("GET /api/v1/blog", s.public(s.handleBlogList))
	mux.HandleFunc("GET /api/v1/blog/{slug}", s.public(s.handleBlogArticle))
}

// handleBlogAsset serves a picture of a listed article. The folder is opened
// as an os.Root, so neither ".." nor a symbolic link can lead out of it.
func (s *Server) handleBlogAsset(w http.ResponseWriter, r *http.Request) {
	slug, file := r.PathValue("slug"), r.PathValue("file")
	if i, _ := s.blogFind(slug); i < 0 || !validAsset(file) {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(path.Join(s.cfg.BlogDir, slug))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", blogAssetTypes[strings.ToLower(path.Ext(file))])
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
