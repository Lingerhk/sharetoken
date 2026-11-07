#!/usr/bin/env python3
"""TEST ONLY: a fast, honest OpenAI-compatible model server for load tests.

It answers every chat request with a deterministic text of N words, streamed
in chunks, and reports usage close to sharetoken's own estimate, so receipts
always pass the buyer's plausibility check. It isolates the network's
overhead (relays, coordinator, encryption, settlement) from model speed.

usage: mock_upstream.py <port> [model ...]          (default model: mock-fast)
env:   MOCK_WORDS     words per answer (default 48; request max_tokens caps it)
       MOCK_DELAY_MS  delay between streamed chunks (default 5)
       MOCK_FAIL_PCT  percent of requests answered with HTTP 500 (default 0)
Works on Python 3.6+ (CentOS 7).
"""
import http.server
import json
import random
import socketserver
import sys
import time

PORT = int(sys.argv[1])
MODELS = sys.argv[2:] or ["mock-fast"]
WORDS = int(__import__("os").environ.get("MOCK_WORDS", "48"))
DELAY = int(__import__("os").environ.get("MOCK_DELAY_MS", "5")) / 1000.0
FAIL = int(__import__("os").environ.get("MOCK_FAIL_PCT", "0"))
VOCAB = "alpha beta gamma delta epsilon zeta theta kappa lambda sigma omega".split()


def est(s):
    return (len(s) + 3) // 4


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _json(self, code, obj):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            self._json(200, {"object": "list", "data": [{"id": m, "object": "model", "owned_by": "mock"} for m in MODELS]})
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        if FAIL and random.randrange(100) < FAIL:
            self._json(500, {"error": {"message": "injected failure"}})
            return
        prompt = sum(4 + est(m.get("content") if isinstance(m.get("content"), str) else json.dumps(m.get("content"))) for m in body.get("messages", []))
        n = WORDS
        if body.get("max_tokens"):
            n = max(1, min(n, int(body["max_tokens"])))
        words = [VOCAB[i % len(VOCAB)] for i in range(n)]
        text = " ".join(words)
        usage = {"prompt_tokens": prompt, "completion_tokens": est(text), "total_tokens": prompt + est(text)}
        model = body.get("model", MODELS[0])
        if not body.get("stream"):
            self._json(200, {"id": "mock", "object": "chat.completion", "model": model,
                             "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                             "usage": usage})
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        for i, w in enumerate(words):
            piece = w if i == 0 else " " + w
            ev = {"id": "mock", "object": "chat.completion.chunk", "model": model,
                  "choices": [{"index": 0, "delta": {"content": piece}, "finish_reason": None}]}
            self.wfile.write(b"data: " + json.dumps(ev).encode() + b"\n\n")
            self.wfile.flush()
            if DELAY:
                time.sleep(DELAY)
        ev = {"id": "mock", "object": "chat.completion.chunk", "model": model,
              "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": usage}
        self.wfile.write(b"data: " + json.dumps(ev).encode() + b"\n\ndata: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True

    def log_message(self, *args):
        pass


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    request_queue_size = 256


Server(("127.0.0.1", PORT), Handler).serve_forever()
