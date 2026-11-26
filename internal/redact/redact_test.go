package redact

import (
	"strings"
	"testing"
)

const (
	selfPeer  = "12D3KooWLtcnr1J6QZ4tLqDMTUGbwPZLmqyBugwuTbHW3RsVzcA7"
	otherPeer = "12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe2Xo"
)

func testRedactor() *Redactor {
	return New("/Users/alice", "alice", "alices-macbook.local", selfPeer)
}

func TestString(t *testing.T) {
	r := testRedactor()
	for _, c := range []struct{ in, want string }{
		{"sign-in for Bob.Smith+x@example.co.uk failed", "sign-in for <email> failed"},
		{"Authorization: Bearer st_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "Authorization: Bearer <secret>"},
		{"token st_0123456789abcdef0123456789abcdef in use", "token <secret> in use"},
		{"key sk-st-AbCdEfGhIjKlMnOpQrSt and sk-ant-api03-xyzxyzxyzxyz", "key <secret> and <secret>"},
		{"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N", "jwt <secret>"},
		{`{"api_key":"abcd1234efgh"}`, `{"api_key":"<secret>"}`},
		{"refresh_token=zzzzyyyyxxxx&x=1", "refresh_token=<secret>&x=1"},
		{"dial https://api.openai.com/v1/chat/completions?key=1: EOF", "dial https://api.openai.com/…: EOF"},
		{"POST https://gpu-box.corp.example.com:8443/v1/x failed", "POST https://<host>:8443/… failed"},
		{"upstream http://127.0.0.1:11434/v1", "upstream http://127.0.0.1:11434/…"},
		{"http://192.168.6.2:8006/x", "http://<lan-ip>:8006/…"},
		{"https://user:pw@sharetoken.app/app/", "https://sharetoken.app/…"},
		{"sharetoken://p#ABCDEFGH", "sharetoken://<lan-host>/…"},
		{"dial tcp 203.0.113.9:443: i/o timeout", "dial tcp <ip>:443: i/o timeout"},
		{"from 10.1.2.3 and 127.0.0.1", "from <lan-ip> and 127.0.0.1"},
		{"/ip4/198.51.100.7/udp/4001/quic-v1/p2p/" + otherPeer, "/ip4/<ip>/udp/4001/quic-v1/p2p/peer#"},
		{"/dns4/relay-us.sharetoken.app/tcp/4001", "/dns4/relay-us.sharetoken.app/tcp/4001"},
		{"/dns4/my-relay.example.net/tcp/4001", "/dns4/<host>/tcp/4001"},
		{"[2001:db8::1]:4001 and fe80::1%en0", "<ip>:4001 and <lan-ip>"},
		{"at 10:20:30 and 2026-11-24T10:20:30+08:00", "at 10:20:30 and 2026-11-24T10:20:30+08:00"},
		{"open /Users/alice/.sharetoken/config.json: denied", "open ~/.sharetoken/config.json: denied"},
		{`C:\Users\bob\AppData\Local\x`, `C:\Users\<user>\AppData\Local\x`},
		{"/home/carol/.sharetoken/logs", "/home/<user>/.sharetoken/logs"},
		{"host alices-macbook.local said no to alice", "host <host> said no to <user>"},
		{"pairing code ABC-DEF expired", "pairing code <code> expired"},
		{"hash 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "hash <secret>"},
		{"me " + selfPeer, "me self:VzcA7"[:0] + "me self:" + selfPeer[len(selfPeer)-6:]},
		{"github.com/libp2p/go-libp2p/p2p/net/swarm.(*Swarm).dialWorkerLoop(0x14000123456)", "github.com/libp2p/go-libp2p/p2p/net/swarm.(*Swarm).dialWorkerLoop(0x14000123456)"},
		{"\tsharetoken/internal/client/agent.go:812 +0x1c4", "\tsharetoken/internal/client/agent.go:812 +0x1c4"},
		{"panic: runtime error: index out of range [3] with length 3", "panic: runtime error: index out of range [3] with length 3"},
		{"version 1.0.5 build", "version 1.0.5 build"},
		{"four numbers look like an address: 1.0.5.2", "four numbers look like an address: <ip>"},
	} {
		got := r.String(c.in)
		if strings.HasSuffix(c.want, "peer#") {
			if !strings.HasPrefix(got, c.want) || strings.Contains(got, otherPeer) {
				t.Errorf("%q\n got %q\nwant prefix %q", c.in, got, c.want)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestPeersLineUpWithinAReport(t *testing.T) {
	r1, r2 := testRedactor(), testRedactor()
	a, b := r1.String(otherPeer), r1.String("x "+otherPeer)
	if a == otherPeer || !strings.HasSuffix(b, a) {
		t.Fatalf("one report: %q %q", a, b)
	}
	if r2.String(otherPeer) == a {
		t.Fatalf("two reports gave the same pseudonym %q (they should not line up)", a)
	}
}

func TestLine(t *testing.T) {
	r := testRedactor()
	for _, c := range []struct{ in, want string }{
		{`time=2026-11-24T10:00:00.000+08:00 level=INFO msg="signed in" component=daemon user=alice@example.com device=` + selfPeer + ` new_account=false`,
			`time=2026-11-24T10:00:00.000+08:00 level=INFO msg="signed in" component=daemon user=… device=… new_account=false`},
		{`time=2026-11-24T10:00:01.000+08:00 level=WARN msg="cannot join the network yet" component=daemon err="dial tcp 203.0.113.9:443: connect: connection refused" retry_in=4s`,
			`time=2026-11-24T10:00:01.000+08:00 level=WARN msg="cannot join the network yet" component=daemon err="dial tcp <ip>:443: connect: connection refused" retry_in=4s`},
		{`time=2026-11-24T10:00:02.000+08:00 level=INFO msg=sharing component=client model=qwen2.5:7b upstream=qwen2.5:7b in=100000 out=200000`,
			`time=2026-11-24T10:00:02.000+08:00 level=INFO msg=sharing component=client model=qwen2.5:7b upstream=qwen2.5:7b in=100000 out=200000`},
		{`time=2026-11-24T10:00:03.000+08:00 level=INFO msg="direct connection without a tunnel" component=tailnet peer=` + selfPeer + ` addr=/ip4/198.51.100.7/tcp/4001`,
			`time=2026-11-24T10:00:03.000+08:00 level=INFO msg="direct connection without a tunnel" component=tailnet peer=self:` + selfPeer[len(selfPeer)-6:] + ` addr=/ip4/<ip>/tcp/4001`},
		{`time=2026-11-24T10:00:04.000+08:00 level=INFO msg=x addr=192.168.1.5:9787 home=/Users/alice/.sharetoken unknown_field="anything at all"`,
			`time=2026-11-24T10:00:04.000+08:00 level=INFO msg=x addr=<lan-ip>:9787 home=… unknown_field=…`},
		{`time=2026-11-24T10:00:05.000+08:00 level=WARN msg=x err="` + strings.Repeat("e ", 200) + `"`,
			`time=2026-11-24T10:00:05.000+08:00 level=WARN msg=x err="` + strings.Repeat("e ", MaxErrLen/2) + `…"`},
		// The loop guard's lines carry tool arguments and what the model wrote.
		{`time=2026-11-24T10:00:06.000+08:00 level=WARN msg="loop guard answered a request stuck in a tool loop" component=buyer api=messages turns=4 calls="Read {\"path\":\"/src/secret plan.md\"}"`,
			`time=2026-11-24T10:00:06.000+08:00 level=WARN msg="loop guard answered a request stuck in a tool loop" component=buyer api=messages turns=4 calls=…`},
		{`time=2026-11-24T10:00:07.000+08:00 level=WARN msg="loop guard stopped an answer" component=buyer api=chat loop="the same sentence the model wrote"`,
			`time=2026-11-24T10:00:07.000+08:00 level=WARN msg="loop guard stopped an answer" component=buyer api=chat loop=…`},
		// Not slog's text: a panic line goes through String.
		{"goroutine 7 [running]: alice@example.com", "goroutine 7 [running]: <email>"},
	} {
		if got := r.Line(c.in); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// FuzzNoLeak puts secrets into arbitrary text: none comes out.
func FuzzNoLeak(f *testing.F) {
	for _, s := range []string{"", "err=", "time=x msg=", "panic: x\ngoroutine 1 [running]:", `msg="a b"`, "https://x/"} {
		f.Add(s, s)
	}
	secrets := []string{"dave@example.org", "st_00112233445566778899aabbccddeeff", "sk-proj-AAAAbbbbCCCCdddd", "203.0.113.77", "/Users/dave/", otherPeer}
	f.Fuzz(func(t *testing.T, a, b string) {
		r := New("/Users/dave", "dave", "", "")
		for _, sec := range secrets {
			// A secret follows a separator in real text: a space, =, :, a quote.
			for _, in := range []string{a + " " + sec + " " + b, a + "=" + sec + "&" + b, a + `:"` + sec + `"` + b} {
				for _, out := range []string{r.String(in), r.Line(in), r.Line("time=2026-11-24T10:00:00Z level=INFO msg=x err=" + strconvQuote(in))} {
					if strings.Contains(out, strings.TrimSuffix(sec, "/")) && !strings.Contains(a+b, sec) {
						t.Fatalf("%q leaked from %q: %q", sec, in, out)
					}
				}
			}
		}
	})
}

func strconvQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func TestTruncate(t *testing.T) {
	if got := Truncate("日本語", 4); got != "日…" {
		t.Fatalf("got %q", got)
	}
	if got := Truncate("abc", 3); got != "abc" {
		t.Fatalf("got %q", got)
	}
}
