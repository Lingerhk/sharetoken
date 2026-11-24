// Package redact removes what must not leave a user's machine from text that
// does: crash reports (internal/diag). The client applies it before a report
// is queued, and the portal applies it again to what it receives.
//
// Two layers:
//   - Line handles one line of the client's log (slog's key=value text): keys
//     on an allow list keep their value, a few kinds of value (addresses,
//     peers, errors) are rewritten, and every other value is dropped.
//   - String rewrites free text, such as a panic message, a stack trace or an
//     error: e-mail addresses, credentials, URLs, IP addresses, home
//     directories and pairing codes.
//
// What it cannot recognize it keeps, so the reports carry only text the
// program itself writes: log lines and Go tracebacks, never a prompt.
package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Redactor rewrites text for one report. The zero value applies the generic
// rules only (the portal's second pass); the client sets the fields below.
type Redactor struct {
	Home string // the user's home directory: written as ~
	User string // the OS account name: written as <user>
	Host string // the machine's name: written as <host>
	Self string // this device's peer ID: kept, shortened

	salt []byte // keys the pseudonyms of other peers: new for each Redactor
}

// New returns a Redactor for one report, with a fresh salt for the peer
// pseudonyms: peers line up within the report, not across reports.
func New(home, user, host, self string) *Redactor {
	r := &Redactor{Home: home, User: user, Host: host, Self: self}
	r.salt = make([]byte, 16)
	_, _ = rand.Read(r.salt)
	return r
}

// MaxErrLen bounds an error value in a log line; an upstream's error can carry
// a piece of its answer, and the start says what went wrong.
const MaxErrLen = 300

var (
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// Credentials: an Authorization value, the API keys of the vendors and of
	// sharetoken (st_ tokens, sk-st- keys), JWTs, and name=value pairs.
	bearerRE = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{6,}`)
	skRE     = regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_\-]{8,}`)
	stTokRE  = regexp.MustCompile(`st_[0-9a-fA-F]{16,}`)
	jwtRE    = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`)
	kvRE     = regexp.MustCompile(`(?i)\b((?:api[_\-]?key|access[_\-]?token|refresh[_\-]?token|id[_\-]?token|token|secret|password|passwd|authorization|x-api-key|x-sharetoken-key|cookie|session)\s*["']?\s*[=:]\s*["']?)([^\s"'&,;]{4,})`)
	// Random-looking runs: hex of 32 and more, and long base64.
	hexRE = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	b64RE = regexp.MustCompile(`[A-Za-z0-9+/_\-]{48,}={0,2}`)

	urlRE = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.\-]{1,15})://([^\s"'<>()\[\]{}]*)`)
	// libp2p peer IDs (base58): Ed25519 keys and older RSA ones.
	peerRE  = regexp.MustCompile(`\b(12D3KooW[1-9A-HJ-NP-Za-km-z]{44}|Qm[1-9A-HJ-NP-Za-km-z]{44})\b`)
	maddrRE = regexp.MustCompile(`/(ip4|ip6|dns|dns4|dns6|dnsaddr)/([^/\s"]+)`)
	ip4RE   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ip6RE   = regexp.MustCompile(`(?i)\[?\b(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}(?:%[0-9a-z]+)?\b\]?`)
	// Home directories: macOS, Linux, Windows (both slashes, any drive).
	homeRE = regexp.MustCompile(`(?i)(/Users/|/home/|[A-Z]:[\\/]+Users[\\/]+)([^/\\\s"']+)`)
	// Pairing codes as people pass them on (accounts.FormatPairingCode).
	codeRE = regexp.MustCompile(`\b[A-Z2-7]{3}-[A-Z2-7]{3}\b|\b[A-Z2-7]{5}-[A-Z2-7]{5}\b`)
)

// keepHosts are public services whose name says which upstream failed and
// tells nothing about the user.
var keepHosts = []string{
	"sharetoken.app", "api.openai.com", "chatgpt.com", "auth.openai.com", "api.anthropic.com", "claude.ai", "console.anthropic.com",
	"api.deepseek.com", "generativelanguage.googleapis.com", "openrouter.ai", "api.moonshot.cn", "api.moonshot.ai", "dashscope.aliyuncs.com",
	"open.bigmodel.cn", "api.minimax.chat", "api.x.ai", "api.mistral.ai", "api.groq.com", "api.together.xyz", "api.siliconflow.cn",
	"github.com", "objects.githubusercontent.com", "login.tailscale.com", "derp.tailscale.com", "controlplane.tailscale.com",
}

func keptHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	for _, k := range keepHosts {
		if h == k || strings.HasSuffix(h, "."+k) {
			return true
		}
	}
	return false
}

// String rewrites free text.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	s = urlRE.ReplaceAllStringFunc(s, r.url)
	s = emailRE.ReplaceAllStringFunc(s, func(m string) string {
		if _, dom, _ := strings.Cut(m, "@"); len(dom) > 1 && dom[0] == 'v' && dom[1] >= '0' && dom[1] <= '9' {
			return m // a Go module version in a path: golang.org/x/net@v0.30.0
		}
		return "<email>"
	})
	s = bearerRE.ReplaceAllString(s, "$1 <secret>")
	s = kvRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := kvRE.FindStringSubmatch(m)
		switch v := strings.ToLower(sub[2]); {
		case strings.HasPrefix(v, "<"), v == "bearer", v == "basic": // already done, or the scheme before it
			return m
		}
		return sub[1] + "<secret>"
	})
	s = skRE.ReplaceAllString(s, "<secret>")
	s = stTokRE.ReplaceAllString(s, "<secret>")
	s = jwtRE.ReplaceAllString(s, "<secret>")
	s = peerRE.ReplaceAllStringFunc(s, r.peer)
	s = maddrRE.ReplaceAllStringFunc(s, maddr)
	s = r.paths(s)
	s = hexRE.ReplaceAllString(s, "<secret>")
	s = b64RE.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Count(m, "/") > 2 || strings.Contains(m, "peer#") { // a path, not a secret
			return m
		}
		return "<secret>"
	})
	s = ip4RE.ReplaceAllStringFunc(s, ip4)
	s = ip6RE.ReplaceAllStringFunc(s, ip6)
	s = codeRE.ReplaceAllString(s, "<code>")
	return s
}

func (r *Redactor) paths(s string) string {
	if r.Home != "" && len(r.Home) > 1 {
		s = strings.ReplaceAll(s, r.Home, "~")
		if strings.Contains(r.Home, `\`) { // Windows paths also appear with forward slashes
			s = strings.ReplaceAll(s, strings.ReplaceAll(r.Home, `\`, "/"), "~")
		}
	}
	s = homeRE.ReplaceAllString(s, "${1}<user>")
	if len(r.User) >= 3 {
		s = replaceWord(s, r.User, "<user>")
	}
	if len(r.Host) >= 3 {
		s = replaceWord(s, r.Host, "<host>")
		if i := strings.IndexByte(r.Host, '.'); i >= 3 {
			s = replaceWord(s, r.Host[:i], "<host>")
		}
	}
	return s
}

// replaceWord replaces old in s where it stands as a word of its own, ignoring
// case: a user named "go" must not turn every "go" into <user>.
func replaceWord(s, old, repl string) string {
	re, err := regexp.Compile(`(?i)(^|[^A-Za-z0-9_\-])` + regexp.QuoteMeta(old) + `($|[^A-Za-z0-9_\-])`)
	if err != nil {
		return s
	}
	for range 3 { // adjacent matches share a separator
		s = re.ReplaceAllString(s, "${1}"+repl+"${2}")
	}
	return s
}

func (r *Redactor) url(m string) string {
	sub := urlRE.FindStringSubmatch(m)
	scheme, rest := strings.ToLower(sub[1]), sub[2]
	// What ends a URL in prose: trailing punctuation stays outside.
	trail := ""
	for len(rest) > 0 && strings.ContainsRune(".,;:!?", rune(rest[len(rest)-1])) {
		trail = rest[len(rest)-1:] + trail
		rest = rest[:len(rest)-1]
	}
	hostport, path := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		hostport, path = rest[:i], rest[i:]
	}
	if i := strings.LastIndexByte(hostport, '@'); i >= 0 { // credentials
		hostport = hostport[i+1:]
	}
	host, port := hostport, ""
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host, port = h, p
	}
	out := scheme + "://" + hostClass(host)
	if port != "" {
		out += ":" + port
	}
	if path != "" && path != "/" {
		out += "/…"
	}
	return out + trail
}

// hostClass names a host without saying which one, unless it is loopback or
// a public service on keepHosts.
func hostClass(host string) string {
	if host == "" {
		return ""
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		switch {
		case a.IsLoopback():
			return host
		case a.IsPrivate() || a.IsLinkLocalUnicast():
			return "<lan-ip>"
		}
		return "<ip>"
	}
	switch h := strings.ToLower(host); {
	case h == "localhost":
		return host
	case keptHost(h):
		return h
	case strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".internal") || !strings.Contains(h, "."):
		return "<lan-host>"
	}
	return "<host>"
}

func (r *Redactor) peer(id string) string {
	if r.Self != "" && id == r.Self {
		return "self:" + id[len(id)-6:]
	}
	key := r.salt
	if key == nil {
		key = []byte("redact")
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id))
	return "peer#" + hex.EncodeToString(m.Sum(nil))[:6]
}

func maddr(m string) string {
	sub := maddrRE.FindStringSubmatch(m)
	proto, val := sub[1], sub[2]
	if a, err := netip.ParseAddr(val); err == nil && a.IsLoopback() {
		return m
	}
	return "/" + proto + "/" + hostClass(val)
}

func ip4(m string) string {
	a, err := netip.ParseAddr(m)
	if err != nil || !a.Is4() {
		return m // not an address (a version number of four parts)
	}
	return hostClass(m)
}

func ip6(m string) string {
	core := strings.Trim(m, "[]")
	if i := strings.IndexByte(core, '%'); i >= 0 {
		core = core[:i]
	}
	a, err := netip.ParseAddr(core)
	if err != nil || !a.Is6() {
		return m // a time of day, or Go's hex offsets
	}
	// Times (10:20:30) parse as IPv6 only with "::" or 8 groups; plain digits
	// with colons are kept unless the text really is an address.
	if !strings.Contains(core, "::") && strings.Count(core, ":") < 7 {
		return m
	}
	if a.IsLoopback() {
		return m
	}
	if a.IsPrivate() || a.IsLinkLocalUnicast() {
		return "<lan-ip>"
	}
	return "<ip>"
}

// ---- log lines ----

// keepKeys keep their value as it is: names, numbers and states the program
// sets itself.
var keepKeys = map[string]bool{
	"time": true, "level": true, "component": true, "kind": true, "status": true, "api": true, "proto": true, "outcome": true,
	"region": true, "took": true, "rtt": true, "retry_in": true, "chunks": true, "frames": true, "turns": true, "calls": true,
	"loop": true, "previous": true, "generate": true, "new_items": true, "items": true, "from": true, "resumes": true,
	"progress_frames": true, "wire_bytes": true, "earned": true, "in": true, "out": true, "model": true, "job": true,
	"new_account": true, "format": true, "version": true, "current": true, "latest": true, "backend": true, "debug": true,
	"expires": true, "transport": true, "official": true, "source": true, "process": true, "state": true, "count": true,
	"attempt": true, "attempts": true, "env": true, "os": true, "arch": true, "mode": true, "phase": true,
	"provider": true, "plan": true, "window": true, "percent": true, "used": true, "reset": true, "times": true,
	"interval": true, "wait": true, "duration": true, "elapsed": true, "bytes": true, "size": true, "n": true, "len": true,
	"response": true, "reports": true, "upstream": true, "other_portal": true, "measurement": true, "port": true, "pid": true, "signal": true, "exit": true,
}

// dropKeys never keep their value: who the user is, and where their files are.
var dropKeys = map[string]bool{
	"user": true, "email": true, "username": true, "device": true, "home": true, "file": true, "path": true, "dir": true,
	"download": true, "notes": true, "name": true, "code": true, "invite": true, "key": true, "token": true, "secret": true,
	"password": true, "prompt": true, "message": true, "content": true, "body": true, "header": true, "headers": true,
}

// peerKeys hold a peer ID (or a multiaddr that ends in one).
var peerKeys = map[string]bool{"peer": true, "node": true, "relay": true, "old": true, "new": true, "coordinator": true, "seller": true, "buyer": true}

// addrKeys hold an address or URL.
var addrKeys = map[string]bool{"addr": true, "url": true, "portal": true, "endpoint": true, "remote": true, "local": true, "listen": true, "host": true, "derp": true}

// textKeys hold program text that may quote something it got.
var textKeys = map[string]bool{"msg": true, "err": true, "error": true, "why": true, "reason": true, "detail": true, "warning": true}

// Line rewrites one line of the client's log. A line that is not key=value
// text (a panic's traceback, a line of the shell) goes through String.
func (r *Redactor) Line(line string) string {
	pairs, ok := parseLogfmt(line)
	if !ok || len(pairs) == 0 || pairs[0].key != "time" {
		return r.String(line)
	}
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p.key)
		b.WriteByte('=')
		b.WriteString(quote(r.value(p.key, p.val)))
	}
	return b.String()
}

func (r *Redactor) value(key, val string) string {
	base := key
	if i := strings.LastIndexByte(key, '.'); i >= 0 { // slog groups: a.b=…
		base = key[i+1:]
	}
	switch {
	case dropKeys[base]:
		return "…"
	case textKeys[base]:
		return Truncate(r.String(val), MaxErrLen)
	case peerKeys[base]:
		return r.String(val)
	case addrKeys[base]:
		if strings.Contains(val, "://") || strings.HasPrefix(val, "/") {
			return r.String(val)
		}
		if h, p, err := net.SplitHostPort(val); err == nil {
			return hostClass(h) + ":" + p
		}
		return hostClass(val)
	case keepKeys[base]:
		return Truncate(r.String(val), 120)
	}
	return "…"
}

type pair struct{ key, val string }

// parseLogfmt splits slog's text output: key=value, values quoted with Go
// syntax when they need it.
func parseLogfmt(s string) ([]pair, bool) {
	var out []pair
	for i := 0; i < len(s); {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			return nil, false
		}
		key := s[i : i+eq]
		if strings.ContainsAny(key, " \"") {
			return nil, false
		}
		i += eq + 1
		var val string
		if i < len(s) && s[i] == '"' {
			end := i + 1
			for end < len(s) {
				if s[end] == '\\' {
					end += 2
					continue
				}
				if s[end] == '"' {
					break
				}
				end++
			}
			if end >= len(s) {
				return nil, false
			}
			v, err := strconv.Unquote(s[i : end+1])
			if err != nil {
				return nil, false
			}
			val, i = v, end+1
		} else {
			end := strings.IndexByte(s[i:], ' ')
			if end < 0 {
				end = len(s) - i
			}
			val, i = s[i:i+end], i+end
		}
		out = append(out, pair{key, val})
	}
	return out, true
}

func quote(v string) string {
	if v == "" || strings.ContainsAny(v, " \"=\\\n\t") || !utf8.ValidString(v) {
		return strconv.Quote(v)
	}
	return v
}

// Truncate cuts s to at most n bytes on a rune boundary, marking the cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
