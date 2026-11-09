#!/usr/bin/env python3
"""Local multi-model proxy in front of self-hosted / OpenAI-compatible servers.

Each upstream is exposed on its own loopback port (127.0.0.1:<port>), so a local
agent dev setup can point different model clients at different ports while a
single process forwards every request, injecting the right bearer token and
optionally renaming models. Streams (SSE) pass through chunk by chunk.
Stdlib only, Python 3.6+.

usage: upstream_proxy.py [config.json]      (default: upstream_proxy.json beside
                                             this script, else a built-in single
                                             upstream from env vars)

config.json:
  {
    "defaults": { "timeout": 600, "strip_path_prefix": "/v1" },
    "upstreams": [
      { "name": "deepseek", "port": 11434,
        "url": "https://api.deepseek.com/v1",
        "key_env": "DEEPSEEK_API_KEY", "key": "sk-...",
        "model_map": { "local-name": "remote-name" } }
    ]
  }

per-upstream fields:
  name      label for logs                              (default: host)
  port      loopback port to listen on                  (required)
  url       upstream base URL including the version path (required)
  key       bearer token; literal fallback              (optional)
  key_env   env var holding the bearer token; wins over "key" when set
  model_map rename {request_model: upstream_model}       (optional)
  headers   extra request headers {name: value}         (optional; e.g.
            anthropic-workspace-id for a key not scoped to a workspace)
  headers_env  {header name: env var} values from the environment (win)
  timeout   per-request upstream timeout, seconds        (default: defaults.timeout)
  strip_path_prefix  prefix dropped from the incoming path before it is
            appended to the upstream base path           (default "/v1")

Single-upstream env fallback (no config file): UPSTREAM_URL, UPSTREAM_KEY,
UPSTREAM_MODEL_MAP ("local=remote,..."), listen port via argv[1] or 11434.
"""
import http.client
import http.server
import json
import os
import socketserver
import ssl
import sys
import threading
import urllib.parse

HOP = {"connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade",
       "proxy-authorization", "proxy-authenticate", "host", "content-length"}
HERE = os.path.dirname(os.path.abspath(__file__))
_SSL_CTX = ssl.create_default_context()


class Upstream:
    """Resolved config for one upstream: where to forward and how to rewrite."""

    def __init__(self, cfg, defaults):
        self.port = int(cfg["port"])
        self.base = urllib.parse.urlsplit(cfg["url"].rstrip("/"))
        self.name = cfg.get("name") or self.base.netloc
        self.key = os.environ.get(cfg["key_env"]) if cfg.get("key_env") else None
        if not self.key:
            self.key = cfg.get("key") or ""
        self.model_map = dict(cfg.get("model_map") or {})
        # Extra request headers, e.g. {"anthropic-workspace-id": "wrkspc_..."};
        # headers_env maps a header to the env var holding its value (wins).
        self.headers = {k: str(v) for k, v in (cfg.get("headers") or {}).items()}
        for h, var in (cfg.get("headers_env") or {}).items():
            if os.environ.get(var):
                self.headers[h] = os.environ[var]
        self.timeout = int(cfg.get("timeout", defaults.get("timeout", 600)))
        self.strip = cfg.get("strip_path_prefix", defaults.get("strip_path_prefix", "/v1"))

    def connect(self):
        if self.base.scheme == "https":
            return http.client.HTTPSConnection(self.base.netloc, timeout=self.timeout, context=_SSL_CTX)
        return http.client.HTTPConnection(self.base.netloc, timeout=self.timeout)

    def upstream_path(self, path):
        if self.strip and path.startswith(self.strip):
            path = path[len(self.strip):]
        return self.base.path + path


def make_handler(up):
    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def handle(self):
            # A client hanging up between requests is normal, not a server error.
            try:
                super().handle()
            except (BrokenPipeError, ConnectionResetError):
                pass

        def log_message(self, fmt, *args):
            sys.stderr.write("[%s] %s %s\n" % (up.name, self.command, self.path))

        def _fail(self, status, message):
            msg = json.dumps({"error": {"message": message}}).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            try:
                self.wfile.write(msg)
            except (BrokenPipeError, ConnectionResetError):
                pass

        def handle_any(self):
            n = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(n) if n else None
            if body and up.model_map:
                try:
                    obj = json.loads(body)
                    if obj.get("model") in up.model_map:
                        obj["model"] = up.model_map[obj["model"]]
                        body = json.dumps(obj).encode()
                except ValueError:
                    pass
            headers = {k: v for k, v in self.headers.items()
                       if k.lower() not in HOP and k.lower() != "authorization"}
            if up.key:
                headers["Authorization"] = "Bearer " + up.key
            headers.update(up.headers)
            if body is not None:
                headers["Content-Length"] = str(len(body))
            conn = None
            try:
                conn = up.connect()
                conn.request(self.command, up.upstream_path(self.path), body=body, headers=headers)
                resp = conn.getresponse()
            except Exception as e:  # upstream down: surface a clean 502
                if conn:
                    conn.close()
                self._fail(502, "upstream unreachable: %s" % e)
                return
            self.send_response(resp.status)
            for k, v in resp.getheaders():
                if k.lower() not in HOP:
                    self.send_header(k, v)
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            try:
                while True:
                    chunk = resp.read1(8192) if hasattr(resp, "read1") else resp.read(8192)
                    if not chunk:
                        break
                    self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))
                    self.wfile.flush()
                self.wfile.write(b"0\r\n\r\n")
            except (BrokenPipeError, ConnectionResetError):
                pass  # client cancelled; drop the upstream request too
            finally:
                conn.close()

        do_GET = do_POST = do_PUT = do_DELETE = handle_any

    return Handler


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def load_upstreams(argv):
    """Return (list[Upstream], defaults). Config file if present, else env fallback."""
    path = argv[1] if len(argv) > 1 else os.path.join(HERE, "upstream_proxy.json")
    if os.path.isfile(path):
        with open(path) as f:
            cfg = json.load(f)
        defaults = cfg.get("defaults", {})
        ups = [Upstream(u, defaults) for u in cfg.get("upstreams", [])]
        if not ups:
            sys.exit("no upstreams defined in %s" % path)
        return ups
    # env fallback: single upstream, legacy behaviour
    port = int(argv[1]) if len(argv) > 1 and argv[1].isdigit() else 11434
    return [Upstream({
        "name": "env",
        "port": port,
        "url": os.environ.get("UPSTREAM_URL", "https://api.deepseek.com/v1"),
        "key": os.environ.get("UPSTREAM_KEY", ""),
        "model_map": dict(p.split("=", 1)
                          for p in os.environ.get("UPSTREAM_MODEL_MAP", "").split(",") if "=" in p),
    }, {})]


def serve(up):
    srv = Server(("127.0.0.1", up.port), make_handler(up))
    print("proxy 127.0.0.1:%d -> %s%s  [%s]" % (up.port, up.base.netloc, up.base.path, up.name),
          file=sys.stderr)
    srv.serve_forever()


if __name__ == "__main__":
    upstreams = load_upstreams(sys.argv)
    ports = [u.port for u in upstreams]
    if len(set(ports)) != len(ports):
        sys.exit("duplicate ports in config: %s" % ports)
    # one server thread per upstream; keep the last on the main thread
    for up in upstreams[:-1]:
        threading.Thread(target=serve, args=(up,), daemon=True).start()
    try:
        serve(upstreams[-1])
    except KeyboardInterrupt:
        print("shutting down", file=sys.stderr)
