#!/usr/bin/env python3
"""Functional test of a deployed environment: scripts/functest.py <dev|prod>.

Runs real clients on this Mac against the live portal, coordinator and relays,
with the mock model (scripts/mock_upstream.py): sign-up by email, login, version
checks, a seller and a buyer trading through a relay, the admin console's API,
and the desktop daemon's API. Not a load test: a few requests.

It creates accounts named functest-*, and leaves them. No statistics are sent.
"""
import base64, hashlib, hmac, http.cookiejar, json, os, secrets, shutil, struct, subprocess
import sys, tempfile, time, urllib.error, urllib.request

ENV = sys.argv[1]
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CFG = {
    "dev": {"portal": "https://dev.sharetoken.app", "ssh": "s0nnet@192.168.6.148", "dir": "sharetoken-dev", "db": "sharetoken_dev",
            "mail": "support-dev@sharetoken.app", "mail_unit": "sharetoken-dev-mail", "api": 19887, "seller": 19888, "daemon": 19889, "mock": 19890},
    "prod": {"portal": "https://sharetoken.app", "ssh": "s0nnet@192.168.6.91", "dir": "sharetoken-prod", "db": "sharetoken",
             "mail": "support@sharetoken.app", "mail_unit": "sharetoken-mail", "api": 19787, "seller": 19788, "daemon": 19789, "mock": 19790},
}[ENV]
PORTAL, MODEL = CFG["portal"], "functest-mock"
WEB = {"Origin": PORTAL, "X-Sharetoken-Web": "1"}
RUN = secrets.token_hex(3)
PASSWORD = "functest-" + secrets.token_hex(8)
work = tempfile.mkdtemp(prefix="st-functest-")
procs, passed, failed = [], 0, []


def ok(cond, what, detail=""):
    global passed
    if cond:
        passed += 1
        print("  ok    " + what)
    else:
        failed.append(what)
        print("  FAIL  " + what + ("  -> " + str(detail)[:300] if detail != "" else ""))


def section(name):
    print("\n" + name)


def ssh(cmd, host=None, stdin=None):
    r = subprocess.run(["ssh", "-o", "LogLevel=ERROR", "-o", "ConnectTimeout=10", host or CFG["ssh"], cmd],
                       input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
    if r.returncode:
        raise RuntimeError("ssh %s: %s" % (cmd[:60], r.stderr[:300]))
    return r.stdout


def portal_cmd(args, password=None):
    env = "SHARETOKEN_PASSWORD=%s " % password if password else ""
    return ssh("cd ~/%s && %s./portal -config portal.yaml %s" % (CFG["dir"], env, args))


def sql(q):
    cmd = "cd ~/sharetoken-prod && docker exec st-mysql mysql -h127.0.0.1 -uroot -p\"$(cat mysql-root.pass)\" -N --default-character-set=utf8mb4 %s -e %s 2>/dev/null"
    return ssh(cmd % (CFG["db"], json.dumps(q).replace("$", "\\$").replace("`", "\\`")), host="s0nnet@192.168.6.91").strip()


def call(method, url, body=None, headers=None, opener=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=dict(headers or {}))
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with (opener.open if opener else urllib.request.urlopen)(req, timeout=60) as r:
            raw, status, hdr = r.read(), r.status, r.headers
    except urllib.error.HTTPError as e:
        raw, status, hdr = e.read(), e.code, e.headers
    try:
        return status, json.loads(raw), hdr
    except ValueError:
        return status, raw.decode("utf8", "replace"), hdr


def totp(secret):
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    d = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
    o = d[-1] & 15
    return "%06d" % ((struct.unpack(">I", d[o:o + 4])[0] & 0x7fffffff) % 1000000)


def wait(fn, seconds, every=1):
    end = time.time() + seconds
    while time.time() < end:
        try:
            v = fn()
            if v:
                return v
        except Exception as e:
            print("        (waiting: %r)" % (e,)) if os.environ.get("FUNCTEST_DEBUG") else None
        time.sleep(every)
    return None


def start(args, log, env=None):
    f = open(os.path.join(work, log), "w")
    p = subprocess.Popen(args, stdout=f, stderr=subprocess.STDOUT, env=env)
    procs.append(p)
    return p


def main():
    section("build client (%s)" % ENV)
    ld = subprocess.check_output([ROOT + "/scripts/ldflags.sh"], universal_newlines=True).strip()
    version = subprocess.check_output([ROOT + "/scripts/version.sh"], universal_newlines=True).strip()
    bin_ = os.path.join(work, "sharetoken")
    subprocess.check_call(["go", "build", "-ldflags", ld + " -X sharetoken/client.buildType=sharetoken-build-type=" + ENV, "-o", bin_, "./cmd/sharetoken"], cwd=ROOT)
    out = subprocess.check_output([bin_, "version"], universal_newlines=True)
    ok(version in out and "env " + ENV in out, "client says %s, %s" % (version, ENV), out)
    env = dict(os.environ, SHARETOKEN_TELEMETRY="0", SHARETOKEN_PASSWORD=PASSWORD)

    section("versions")
    st, v, hdr = call("GET", PORTAL + "/api/v1/version")
    ok(st == 200 and v["api"] == 1, "GET /api/v1/version", v)
    server = v["server"]["version"]
    print("        portal " + server)
    ok(hdr.get("X-ST-Server-Version") == server, "responses carry the server version", hdr.get("X-ST-Server-Version"))
    ok(v["schema"].get("core") == 1 and v["schema"].get("mail") == 1, "schema versions reported", v["schema"])
    ok(v["client"].get("min") == "1.0.0" and v["client"].get("latest"), "client policy published", v["client"])
    ok(sql("SELECT COUNT(*) FROM schema_migrations") == "2", "schema_migrations has core and mail")

    section("pages")
    for path, mark in (("/", "ShareToken"), ("/app/", ""), ("/admin/", ""), ("/static/favicon.svg", "")):
        st, body, _ = call("GET", PORTAL + path)
        ok(st == 200 and mark in str(body), "GET " + path, st)

    section("version gate")
    cred = {"username": "nobody", "password": "wrong-password"}
    st, b, _ = call("POST", PORTAL + "/api/v1/login", cred, {"X-ST-Client-Version": "0.9.0"})
    ok(st == 426 and b["error"]["code"] == "upgrade_required", "an old client gets 426", (st, b))
    st, b, _ = call("POST", PORTAL + "/api/v1/login", cred)
    ok(st == 426, "a client without a version gets 426", (st, b))
    st, b, _ = call("POST", PORTAL + "/api/v1/login", cred, {"X-ST-Client-Version": "1.0.0-dev.abc"})
    ok(st == (426 if ENV == "prod" else 401), "a development build: %s" % ("refused" if ENV == "prod" else "served"), (st, b))
    st, b, _ = call("POST", PORTAL + "/api/v1/login", cred, {"X-ST-Client-Version": "1.0.0"})
    ok(st == 401, "the current release passes the gate (then fails on the password)", (st, b))

    section("email sign-up on the web page")
    email = "functest+%s@example.com" % RUN
    cj = http_opener()
    # A code is wiped from the outbox once sent: hold the mail server to read it.
    ssh("sudo systemctl stop " + CFG["mail_unit"])
    st, b, _ = call("POST", PORTAL + "/api/v1/register/code", {"email": email}, WEB, cj)
    ok(st in (200, 201, 202), "a code is requested without a client version", (st, b))
    subject = wait(lambda: sql("SELECT subject FROM mail_outbox WHERE to_addr='%s' ORDER BY id DESC LIMIT 1" % email), 20, 2) or ""
    code = subject.split("：")[-1].split(":")[-1].strip()
    ok(code.isdigit() and len(code) == 6, "the code was queued for mailing", subject)
    ssh("sudo systemctl start " + CFG["mail_unit"])
    st, b, _ = call("POST", PORTAL + "/api/v1/web/register", {"email": email, "password": PASSWORD, "code": code}, WEB, cj)
    ok(st == 201, "sign-up with the code", (st, b))
    st, me, _ = call("GET", PORTAL + "/api/v1/me", None, WEB, cj)
    ok(st == 200 and me["user"]["username"] == email and me["balance"]["available_micros"] > 0, "signed in, with sign-up credit", me)
    state = wait(lambda: sql("SELECT status FROM mail_outbox WHERE to_addr='%s' ORDER BY id DESC LIMIT 1" % email) not in ("", "queued", "sending"), 60, 3)
    print("        mail state: " + sql("SELECT status FROM mail_outbox WHERE to_addr='%s' ORDER BY id DESC LIMIT 1" % email) + " (example.com takes no mail)")
    ok(state, "the mail server processed the message")

    section("accounts")
    seller, buyer = "functest-seller-" + RUN, "functest-buyer-" + RUN
    for u in (seller, buyer):
        portal_cmd("useradd " + u, password=PASSWORD)
    portal_cmd("grant %s 2000000 functest" % buyer)
    sh, bh = os.path.join(work, "seller"), os.path.join(work, "buyer")
    r = subprocess.run([bin_, "-home", bh, "login", "-user", buyer, "-device", "functest-buyer"], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, universal_newlines=True)
    ok(r.returncode == 0, "the client logs in and binds its device", r.stdout)
    r = subprocess.run([bin_, "-home", sh, "login", "-user", seller, "-device", "functest-seller"], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, universal_newlines=True)
    ok(r.returncode == 0, "the seller logs in", r.stdout)
    conf = json.load(open(os.path.join(bh, "config.json")))
    ok(conf.get("version") == 1 and conf.get("env") == ENV, "config.json carries its format version", conf.get("version"))

    section("trade through a relay")
    start(["python3", ROOT + "/scripts/mock_upstream.py", str(CFG["mock"]), MODEL], "mock.log")
    wait(lambda: call("GET", "http://127.0.0.1:%d/v1/models" % CFG["mock"])[0] == 200, 10)
    for a in (["share", "upstream", "http://127.0.0.1:%d/v1" % CFG["mock"]], ["share", "add", MODEL, "-id", MODEL, "-in", "1000", "-out", "2000"], ["share", "on"]):
        subprocess.check_call([bin_, "-home", sh] + a, env=env, stdout=subprocess.DEVNULL)
    start([bin_, "-home", sh, "up", "-http", "127.0.0.1:%d" % CFG["seller"]], "seller.log", env)
    start([bin_, "-home", bh, "up", "-http", "127.0.0.1:%d" % CFG["api"]], "buyer.log", env)
    api = "http://127.0.0.1:%d" % CFG["api"]
    status = wait(lambda: (lambda s: s if s["coordinator"]["connected"] else None)(call("GET", api + "/api/v1/status")[1]), 60)
    ok(status, "the buyer reaches the coordinator")
    if status:
        ok(status["version"] == version, "the local status reports the version", status["version"])
        print("        via " + str(status["coordinator"].get("via"))[:90])
    models = wait(lambda: [m for m in call("GET", api + "/api/v1/models")[1]["models"] if m["model"] == MODEL], 60)
    ok(models, "the seller's model is on the market", models)
    req = {"model": MODEL, "messages": [{"role": "user", "content": "say hello"}], "max_tokens": 16}
    st, b, hdr = call("POST", api + "/v1/chat/completions", req)
    ok(st == 200 and b["choices"][0]["message"]["content"], "a chat completion through the network", (st, b))
    st, raw, _ = call("POST", api + "/v1/chat/completions", dict(req, stream=True))
    ok(st == 200 and "[DONE]" in str(raw), "a streamed completion", (st, str(raw)[:200]))
    def final():
        j = call("GET", api + "/api/v1/jobs?limit=5")[1]
        return j if len(j.get("jobs", [])) >= 2 and all(x["status"] == "final" for x in j["jobs"][:2]) else None
    jobs = wait(final, 60, 2)
    ok(jobs, "both jobs settled", call("GET", api + "/api/v1/jobs?limit=5")[1])
    st, acct, _ = call("GET", api + "/api/v1/account")
    ok(st == 200 and acct["usage"]["bought"]["jobs"] >= 2, "the buyer is billed", acct.get("usage"))
    st, sacct, _ = call("GET", "http://127.0.0.1:%d/api/v1/account" % CFG["seller"])
    ok(st == 200 and sacct["usage"]["sold"]["jobs"] >= 2 and sacct["usage"]["sold"]["micros"] > 0, "the seller is paid", sacct.get("usage"))
    ok(sql("SELECT client_version FROM devices WHERE name='functest-seller' ORDER BY created_at DESC LIMIT 1") == version, "the seller's version is recorded")
    ok(sql("SELECT client_version FROM devices WHERE name='functest-buyer' ORDER BY created_at DESC LIMIT 1") == version, "the buyer's version is recorded")

    section("admin console")
    admin = "functest-admin-" + RUN
    portal_cmd("useradd -admin " + admin, password=PASSWORD)
    op = http_opener()
    hdrs = WEB
    st, ch, _ = call("POST", PORTAL + "/api/v1/web/login", {"username": admin, "password": PASSWORD}, hdrs, op)
    ok(ch.get("challenge") and ch["error"]["code"] == "mfa_setup_required", "an admin must enroll two-factor", ch)
    st, setup, _ = call("POST", PORTAL + "/api/v1/web/mfa/setup", {"challenge": ch["challenge"]}, hdrs, op)
    st, _, _ = call("POST", PORTAL + "/api/v1/web/mfa/confirm", {"challenge": ch["challenge"], "code": totp(setup["secret"])}, hdrs, op)
    ok(st == 200, "two-factor enrollment", st)
    st, rep, _ = call("GET", PORTAL + "/api/v1/admin/versions", None, hdrs, op)
    ok(st == 200, "GET /api/v1/admin/versions", rep)
    if st == 200:
        print("        coordinator %s, mail %s" % (rep.get("coordinator"), rep.get("mail")))
        print("        relays " + ", ".join("%s=%s" % (r["name"], r.get("version")) for r in rep["relays"] if r["enabled"]))
        print("        devices " + str(rep["devices"]))
        ok(rep.get("coordinator") == server, "the coordinator's version shows", rep.get("coordinator"))
        ok(rep.get("mail") == server, "the mail server's version shows", rep.get("mail"))
        ok(all(r.get("version") == server for r in rep["relays"] if r["enabled"]), "every enabled relay runs this release", [(r["name"], r.get("version")) for r in rep["relays"]])
        ok(any(r["coordinator_ok"] for r in rep["relays"]), "the coordinator reaches the relays")
        ok(any(d["version"] == version for d in rep["devices"]), "the client distribution shows this client", rep["devices"])
    st, nodes, _ = call("GET", PORTAL + "/api/v1/admin/nodes", None, hdrs, op)
    ok(st == 200 and any(n.get("client_version") == version for n in nodes["nodes"]), "the node list shows versions", nodes)
    st, ov, _ = call("GET", PORTAL + "/api/v1/admin/overview", None, hdrs, op)
    ok(st == 200 and ov.get("books_balance") is True, "the overview loads, books balance", ov)
    st, mailst, _ = call("GET", PORTAL + "/api/v1/admin/mail/status", None, hdrs, op)
    ok(st == 200 and mailst["overview"]["online"], "the mail server is online", mailst)
    st, pol, _ = call("PUT", PORTAL + "/api/v1/admin/version-policy", {"min_client": "bad"}, hdrs, op)
    ok(st == 400, "a bad version policy is refused", (st, pol))
    ok(sql("SELECT JSON_EXTRACT(value,'$.min_client') FROM meta WHERE `key`='version_policy'").strip('"') == "1.0.0", "the policy did not change")

    section("desktop daemon")
    dh = os.path.join(work, "daemon")
    denv = dict(env)
    denv.pop("SHARETOKEN_PASSWORD", None)
    start([bin_, "-home", dh, "daemon", "-http", "127.0.0.1:%d" % CFG["daemon"]], "daemon.log", denv)
    d = "http://127.0.0.1:%d" % CFG["daemon"]
    s = wait(lambda: call("GET", d + "/api/v1/session")[1], 15)
    ok(s and s["version"] == version and s["api_version"] == 1 and s["state"] == "logged_out", "the session says what it is", s)
    st, page, _ = call("GET", d + "/")
    ok(st == 200, "the app's page loads", st)
    time.sleep(2)
    s = call("GET", d + "/api/v1/session")[1]
    print("        update " + str(s.get("update")))
    ok(s["update"].get("latest"), "the daemon read the release information", s["update"])
    st, pr, _ = call("POST", d + "/api/v1/portal/probe", {})
    ok(st == 200 and pr["portal"] == PORTAL, "the daemon probes the built-in portal", (st, pr))
    st, lg, _ = call("POST", d + "/api/v1/login", {"pin": "", "account": email, "password": PASSWORD, "device": "functest-daemon"})
    ok(st == 200 and lg.get("logged_in"), "login through the daemon", (st, lg))
    ok(wait(lambda: call("GET", d + "/api/v1/session")[1]["state"] == "running", 60), "the daemon joins the network", call("GET", d + "/api/v1/session")[1].get("state"))
    st, _, _ = call("GET", d + "/api/session")
    ok(st == 404 or st == 405, "the unversioned path is gone", st)
    st, lo, _ = call("POST", d + "/api/v1/logout", {})
    ok(st == 200, "logout", (st, lo))


def http_opener():
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


try:
    main()
finally:
    for p in procs:
        p.terminate()
    for p in procs:
        try:
            p.wait(10)
        except subprocess.TimeoutExpired:
            p.kill()
    if failed:
        print("\nlogs kept in " + work)
    else:
        shutil.rmtree(work, ignore_errors=True)
print("\n%d passed, %d failed" % (passed, len(failed)))
for f in failed:
    print("  FAILED: " + f)
sys.exit(1 if failed else 0)
