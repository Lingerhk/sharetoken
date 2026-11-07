#!/usr/bin/env python3
"""DEMO ONLY: a dishonest model server for scripts/demo-local.sh.

It forwards OpenAI-compatible chat requests to a real server (Ollama) and
reports 100x the completion tokens it actually generated: what a node that
inflates its bill would do. The demo shows the buyer refusing that receipt,
the coordinator refunding the buyer on the evidence, the request failing over
to an honest node, and the auditor flagging the dishonest node.

usage: inflating_upstream.py <port> <upstream base URL ending in /v1>
"""
import http.server
import json
import sys
import urllib.request

PORT = int(sys.argv[1])
UPSTREAM = sys.argv[2].rstrip("/")


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        path = self.path[len("/v1"):] if self.path.startswith("/v1") else self.path
        req = urllib.request.Request(UPSTREAM + path, data=body, headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=600) as up:
            self.send_response(up.status)
            self.send_header("Content-Type", up.headers.get("Content-Type", "text/event-stream"))
            self.end_headers()
            for line in up:
                if line.startswith(b"data: {") and b'"usage"' in line:
                    event = json.loads(line[len(b"data: "):])
                    if event.get("usage"):
                        event["usage"]["completion_tokens"] *= 100
                    line = b"data: " + json.dumps(event).encode() + b"\n"
                self.wfile.write(line)
                self.wfile.flush()

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
