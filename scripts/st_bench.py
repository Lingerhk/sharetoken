#!/usr/bin/env python3
"""sharetoken client test kit: API functional checks and load generation
against a running `sharetoken up` endpoint (default http://127.0.0.1:9787).

  st_bench.py func  [-b BASE] -m MODEL [--tools]           functional suite
  st_bench.py load  [-b BASE] -m MODEL -c CONC (-n N | -d SECS) [--stream]
                    [--max-tokens K] [--prompt TEXT] [--json OUT]

Standard library only, Python 3.6+ (CentOS 7 ships 3.6).
"""
import argparse
import collections
import http.client
import json
import sys
import threading
import time
import urllib.parse

DEFAULT_BASE = "http://127.0.0.1:9787"


def conn_for(base, timeout=600):
    u = urllib.parse.urlparse(base)
    return http.client.HTTPConnection(u.hostname, u.port or 80, timeout=timeout)


def post(base, path, body, headers=None, raw=None, timeout=600):
    c = conn_for(base, timeout)
    data = raw if raw is not None else json.dumps(body).encode()
    h = {"Content-Type": "application/json"}
    h.update(headers or {})
    t0 = time.time()
    c.request("POST", path, body=data, headers=h)
    r = c.getresponse()
    out = r.read()
    c.close()
    return r.status, dict(r.getheaders()), out, time.time() - t0


def get(base, path, headers=None):
    c = conn_for(base, 60)
    c.request("GET", path, headers=headers or {})
    r = c.getresponse()
    out = r.read()
    c.close()
    return r.status, out


def stream(base, body, abort_after=None, timeout=600):
    """Returns dict(status, ttft, total, chunks, text, trailer, error, done, usage)."""
    c = conn_for(base, timeout)
    body = dict(body, stream=True)
    t0 = time.time()
    c.request("POST", "/v1/chat/completions", body=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    r = c.getresponse()
    res = {"status": r.status, "ttft": None, "chunks": 0, "text": "", "trailer": "", "error": None, "done": False, "usage": None}
    if r.status != 200:
        res["error"] = r.read().decode(errors="replace")
        res["total"] = time.time() - t0
        c.close()
        return res
    while True:
        line = r.fp.readline()
        if not line:
            break
        line = line.decode(errors="replace").rstrip("\r\n")
        if line.startswith(":"):
            res["trailer"] = line
            continue
        if not line.startswith("data:"):
            continue
        payload = line[5:].strip()
        if payload == "[DONE]":
            res["done"] = True
            break
        ev = json.loads(payload)
        if "error" in ev:
            res["error"] = ev["error"]
            continue
        if res["ttft"] is None:
            res["ttft"] = time.time() - t0
        res["chunks"] += 1
        for ch in ev.get("choices") or []:
            res["text"] += (ch.get("delta") or {}).get("content") or ""
        if ev.get("usage"):
            res["usage"] = ev["usage"]
        if abort_after and res["chunks"] >= abort_after:
            c.close()
            res["aborted"] = True
            break
    res["total"] = time.time() - t0
    c.close()
    return res


# ---------------------------------------------------------------- functional

class Suite:
    def __init__(self, base, model):
        self.base, self.model, self.results = base, model, []

    def check(self, name, fn):
        t0 = time.time()
        try:
            detail = fn() or ""
            ok = True
        except Exception as e:  # noqa
            ok, detail = False, "%s: %s" % (type(e).__name__, e)
        dt = time.time() - t0
        self.results.append({"name": name, "ok": ok, "secs": round(dt, 2), "detail": str(detail)[:300]})
        print("%-4s %-38s %6.2fs  %s" % ("PASS" if ok else "FAIL", name, dt, str(detail)[:160]))
        sys.stdout.flush()

    def chat(self, messages, **kw):
        body = {"model": self.model, "messages": messages}
        body.update(kw)
        st, h, out, dt = post(self.base, "/v1/chat/completions", body)
        return st, h, out

    def ok_chat(self, messages, **kw):
        st, h, out = self.chat(messages, **kw)
        assert st == 200, "HTTP %d %s" % (st, out[:200])
        j = json.loads(out)
        assert h.get("X-Sharetoken-Verified") == "true", "not verified: %s" % h
        msg = j["choices"][0]["message"]
        return j, h, msg


def run_func(a):
    s = Suite(a.base, a.model)
    m = a.model

    def models():
        st, out = get(a.base, "/v1/models")
        assert st == 200, st
        ids = [d["id"] for d in json.loads(out)["data"]]
        assert m in ids, ids
        return "%d models: %s" % (len(ids), ",".join(ids))
    s.check("GET /v1/models", models)

    def basic():
        j, h, msg = s.ok_chat([{"role": "user", "content": "Reply with the single word: pong"}], max_tokens=32)
        assert msg["content"].strip(), "empty content"
        u = j["usage"]
        assert u["prompt_tokens"] > 0 and u["completion_tokens"] > 0, u
        assert int(h["X-Sharetoken-Cost-Micros"]) > 0
        return "job=%s cost=%s usage=%s/%s %r" % (h["X-Sharetoken-Job"][:8], h["X-Sharetoken-Cost-Micros"], u["prompt_tokens"], u["completion_tokens"], msg["content"][:40])
    s.check("non-stream single turn", basic)

    def streamed():
        r = stream(a.base, {"model": m, "messages": [{"role": "user", "content": "Count from 1 to 10, separated by spaces."}], "max_tokens": 64})
        assert r["status"] == 200 and not r["error"], r
        assert r["done"] and "verified=true" in r["trailer"], r["trailer"]
        assert r["chunks"] > 1 and r["text"].strip()
        return "ttft=%.2fs total=%.2fs chunks=%d %s" % (r["ttft"], r["total"], r["chunks"], r["trailer"][:80])
    s.check("stream", streamed)

    def stream_usage():
        r = stream(a.base, {"model": m, "messages": [{"role": "user", "content": "Say hi."}], "max_tokens": 16, "stream_options": {"include_usage": True}})
        assert r["status"] == 200 and not r["error"] and r["done"], r
        assert r["usage"] and r["usage"].get("completion_tokens"), r["usage"]
        return "usage=%s" % r["usage"]
    s.check("stream + include_usage", stream_usage)

    def multi():
        j, h, msg = s.ok_chat([
            {"role": "system", "content": "You are a terse assistant. Answer in at most five words."},
            {"role": "user", "content": "My name is Zorblat."},
            {"role": "assistant", "content": "Nice to meet you, Zorblat."},
            {"role": "user", "content": "What is my name?"}], max_tokens=24, temperature=0)
        return repr(msg["content"][:60])
    s.check("multi-turn + system", multi)

    def maxtok():
        j, h, msg = s.ok_chat([{"role": "user", "content": "Write a long story about the sea."}], max_tokens=5)
        fr = j["choices"][0]["finish_reason"]
        ct = j["usage"]["completion_tokens"]
        assert ct <= 8, "completion_tokens=%d for max_tokens=5" % ct
        return "finish=%s completion_tokens=%d" % (fr, ct)
    s.check("max_tokens=5 honoured", maxtok)

    def cjk():
        j, h, msg = s.ok_chat([{"role": "user", "content": "用一句中文介绍你自己。"}], max_tokens=48)
        assert msg["content"].strip()
        return repr(msg["content"][:30])
    s.check("CJK prompt", cjk)

    def longp():
        text = ("The quick brown fox jumps over the lazy dog. " * 160)
        j, h, msg = s.ok_chat([{"role": "user", "content": text + "\nHow many words are in the sentence above that is repeated? Answer briefly."}], max_tokens=16)
        return "prompt_tokens=%d" % j["usage"]["prompt_tokens"]
    s.check("long prompt (~7 KB)", longp)

    def params():
        j, h, msg = s.ok_chat([{"role": "user", "content": "Pick a random fruit."}], max_tokens=16, temperature=0.0, top_p=0.9, seed=42, stop=["\n\n"])
        return repr(msg["content"][:40])
    s.check("sampling params (temp/top_p/seed/stop)", params)

    def jsonmode():
        j, h, msg = s.ok_chat([{"role": "user", "content": "Return a JSON object with keys a=1 and b=2. Output only JSON."}], max_tokens=48, response_format={"type": "json_object"})
        c = msg["content"].strip().strip("`")
        if c.startswith("json"):
            c = c[4:]
        json.loads(c)
        return c[:60]
    s.check("response_format json_object", jsonmode)

    if a.tools:
        def tools():
            j, h, msg = s.ok_chat([{"role": "user", "content": "What's the weather in Paris? Use the tool."}], max_tokens=128, tools=[{
                "type": "function", "function": {"name": "get_weather", "description": "Get weather for a city",
                                                 "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}])
            return "tool_calls=%s content=%r" % (msg.get("tool_calls"), (msg.get("content") or "")[:40])
        s.check("tools (function calling)", tools)

    def unknown():
        st, h, out = s.chat([{"role": "user", "content": "x"}], model="no-such-model-xyz")
        assert st == 503, (st, out[:200])
        return out[:100].decode()
    s.check("unknown model -> 503 no_offers", unknown)

    def badjson():
        st, h, out, _ = post(a.base, "/v1/chat/completions", None, raw=b"{not json")
        assert st == 400, (st, out)
        return out[:80].decode()
    s.check("malformed JSON -> 400", badjson)

    def nomodel():
        st, h, out, _ = post(a.base, "/v1/chat/completions", {"messages": [{"role": "user", "content": "x"}]})
        assert st == 400, (st, out)
        return out[:80].decode()
    s.check("missing model -> 400", nomodel)

    def nomsg():
        st, h, out, _ = post(a.base, "/v1/chat/completions", {"model": m, "messages": []})
        assert st in (400, 502), (st, out[:200])
        return "HTTP %d %s" % (st, out[:100].decode())
    s.check("empty messages rejected", nomsg)

    def big():
        st, h, out, _ = post(a.base, "/v1/chat/completions", None, raw=b'{"model":"%s","messages":[{"role":"user","content":"' % m.encode() + b"a" * (5 << 20) + b'"}]}')
        assert st == 413, (st, out[:200])
        return "HTTP 413"
    s.check("5 MB body -> 413", big)

    def origin():
        st, h, out, _ = post(a.base, "/v1/chat/completions", {"model": m, "messages": [{"role": "user", "content": "x"}]}, headers={"Origin": "https://evil.example"})
        assert st == 403, (st, out[:200])
        return "HTTP 403"
    s.check("foreign Origin -> 403", origin)

    def abort():
        r = stream(a.base, {"model": m, "messages": [{"role": "user", "content": "Write 300 words about mountains."}], "max_tokens": 400}, abort_after=3)
        assert r.get("aborted"), r
        time.sleep(2)
        j, h, msg = s.ok_chat([{"role": "user", "content": "Reply: ok"}], max_tokens=8)
        return "aborted after 3 chunks; next request ok (job %s)" % h["X-Sharetoken-Job"][:8]
    s.check("client aborts stream mid-way", abort)

    def parallel4():
        errs, oks = [], []

        def one(i):
            try:
                st, h, out, dt = post(a.base, "/v1/chat/completions", {"model": m, "messages": [{"role": "user", "content": "Say the number %d." % i}], "max_tokens": 16})
                (oks if st == 200 else errs).append((st, round(dt, 2), out[:80]))
            except Exception as e:  # noqa
                errs.append(str(e))
        ts = [threading.Thread(target=one, args=(i,)) for i in range(4)]
        [t.start() for t in ts]
        [t.join() for t in ts]
        assert not errs, errs
        return "4/4 ok, latencies %s" % [o[1] for o in oks]
    s.check("4 parallel requests", parallel4)

    n_ok = sum(r["ok"] for r in s.results)
    print("\n%d/%d passed" % (n_ok, len(s.results)))
    if a.json:
        with open(a.json, "w") as f:
            json.dump({"base": a.base, "model": m, "results": s.results}, f, indent=1)
    return 0 if n_ok == len(s.results) else 1


# ---------------------------------------------------------------- load

def pct(xs, p):
    if not xs:
        return None
    xs = sorted(xs)
    k = min(len(xs) - 1, int(round((len(xs) - 1) * p / 100.0)))
    return round(xs[k], 3)


def run_load(a):
    lock = threading.Lock()
    lat, ttft, codes, nodes = [], [], collections.Counter(), collections.Counter()
    cost = [0]
    tokens = [0]
    unverified = [0]
    started = time.time()
    deadline = started + a.duration if a.duration else None
    remaining = [a.n or 0]
    body = {"model": a.model, "messages": [{"role": "user", "content": a.prompt}], "max_tokens": a.max_tokens}

    def take():
        with lock:
            if deadline:
                return time.time() < deadline
            if remaining[0] <= 0:
                return False
            remaining[0] -= 1
            return True

    reqlog = open(a.log, "a") if a.log else None

    def record(t0, dt, code, msg=""):
        if code != "200" and a.error_pause:
            time.sleep(a.error_pause)  # a real client backs off instead of spinning on errors
        if reqlog:
            msg = " ".join(str(msg).split())[:160]
            with lock:
                reqlog.write("%.3f %.3f %s %s\n" % (t0, dt, code, msg))
                reqlog.flush()

    def slow(t0, dt, what):
        if dt >= a.slow:
            sys.stderr.write("SLOW %s start=%s dur=%.1fs %s\n" % (a.model, time.strftime("%H:%M:%S", time.localtime(t0)), dt, what))
            sys.stderr.flush()

    def worker():
        while take():
            t0 = time.time()
            try:
                if a.stream:
                    r = stream(a.base, body, timeout=a.timeout)
                    slow(t0, r["total"], "ttft=%s trailer=%s err=%s" % (r["ttft"] and round(r["ttft"], 1), r["trailer"][:120], r["error"]))
                    ok = r["status"] == 200 and not r["error"] and r["done"] and "verified=true" in r["trailer"]
                    if ok:
                        record(t0, r["total"], "200")
                    else:
                        e = r["error"]
                        record(t0, r["total"], (e.get("code") if isinstance(e, dict) else None) or "http%d" % r["status"],
                               e.get("message") if isinstance(e, dict) else e)
                    with lock:
                        if ok:
                            lat.append(r["total"])
                            ttft.append(r["ttft"])
                            codes["200"] += 1
                            for kv in r["trailer"].split():
                                if kv.startswith("cost_micros="):
                                    cost[0] += int(kv.split("=")[1])
                                if kv.startswith("node="):
                                    nodes[kv.split("=")[1][-6:]] += 1
                        else:
                            err = r["error"]
                            code = (err.get("code") if isinstance(err, dict) else None) or ("http%d" % r["status"])
                            if r["status"] != 200 and isinstance(err, str):
                                try:
                                    code = json.loads(err)["error"]["code"]
                                except Exception:  # noqa
                                    pass
                            codes[code] += 1
                else:
                    st, h, out, dt = post(a.base, "/v1/chat/completions", body, timeout=a.timeout)
                    slow(t0, dt, "status=%d job=%s node=%s %s" % (st, h.get("X-Sharetoken-Job"), h.get("X-Sharetoken-Node", "")[-6:], out[:100] if st != 200 else ""))
                    code, msg = "200", ""
                    if st != 200:
                        try:
                            err = json.loads(out)["error"]
                            code, msg = err["code"], err.get("message", "")
                        except Exception:  # noqa
                            code, msg = "http%d" % st, out[:160]
                    record(t0, dt, code, msg)
                    with lock:
                        if st == 200:
                            lat.append(dt)
                            codes["200"] += 1
                            cost[0] += int(h.get("X-Sharetoken-Cost-Micros", 0))
                            nodes[h.get("X-Sharetoken-Node", "?")[-6:]] += 1
                            if h.get("X-Sharetoken-Verified") != "true":
                                unverified[0] += 1
                            try:
                                tokens[0] += json.loads(out)["usage"]["completion_tokens"]
                            except Exception:  # noqa
                                pass
                        else:
                            try:
                                code = json.loads(out)["error"]["code"]
                            except Exception:  # noqa
                                code = "http%d" % st
                            codes[code] += 1
            except Exception as e:  # noqa
                slow(t0, time.time() - t0, "exception %r" % e)
                record(t0, time.time() - t0, "exc:" + type(e).__name__, e)
                with lock:
                    codes["exc:" + type(e).__name__] += 1

    ts = [threading.Thread(target=worker) for _ in range(a.c)]
    [t.start() for t in ts]
    [t.join() for t in ts]
    wall = time.time() - started
    total = sum(codes.values())
    res = {
        "model": a.model, "stream": a.stream, "concurrency": a.c, "requests": total, "ok": codes["200"],
        "success_pct": round(100.0 * codes["200"] / total, 2) if total else 0, "wall_s": round(wall, 1),
        "rps": round(codes["200"] / wall, 2), "lat_p50": pct(lat, 50), "lat_p90": pct(lat, 90), "lat_p99": pct(lat, 99),
        "lat_max": round(max(lat), 3) if lat else None, "ttft_p50": pct(ttft, 50), "ttft_p90": pct(ttft, 90),
        "codes": dict(codes), "nodes": dict(nodes), "cost_micros": cost[0], "completion_tokens": tokens[0], "unverified": unverified[0],
    }
    print(json.dumps(res))
    if a.json:
        with open(a.json, "a") as f:
            f.write(json.dumps(res) + "\n")
    return 0


def main():
    p = argparse.ArgumentParser()
    p.add_argument("mode", choices=["func", "load"])
    p.add_argument("-b", "--base", default=DEFAULT_BASE)
    p.add_argument("-m", "--model", required=True)
    p.add_argument("-c", type=int, default=4)
    p.add_argument("-n", type=int)
    p.add_argument("-d", "--duration", type=float)
    p.add_argument("--stream", action="store_true")
    p.add_argument("--tools", action="store_true")
    p.add_argument("--max-tokens", type=int, default=32)
    p.add_argument("--prompt", default="Give one short sentence about the ocean.")
    p.add_argument("--timeout", type=float, default=600)
    p.add_argument("--json")
    p.add_argument("--log", help="append one line per request: start-time duration code")
    p.add_argument("--error-pause", type=float, default=0.2, help="seconds a worker waits after a failed request")
    p.add_argument("--slow", type=float, default=10, help="log requests slower than this (seconds) to stderr")
    a = p.parse_args()
    if a.mode == "load" and not (a.n or a.duration):
        p.error("load needs -n or -d")
    sys.exit(run_func(a) if a.mode == "func" else run_load(a))


if __name__ == "__main__":
    main()
