#!/usr/bin/env python3
"""Publish the mail DNS records of sharetoken.app at GoDaddy (docs/mail.md).

  scripts/mail_dns.py -n                      # show what would change
  scripts/mail_dns.py --dkim stdev1=KEY.pem   # write; --dkim can repeat

Records: A mail (same address as the apex, the production host), MX @,
SPF in TXT @ (the dev host sends directly), one DKIM TXT per key, and Resend's
records (prod sends through Resend: its DKIM key and the CNAMEs of its return path,
send and rsend, which carry the SPF it is checked against). TXT @ is rewritten keeping any other
values. _dmarc is GoDaddy's default and is left alone. Token: config.env.
"""
import argparse
import base64
import json
import os
import pathlib
import re
import subprocess
import sys
import urllib.error
import urllib.request

DOMAIN = "sharetoken.app"
MAIL_HOST = "mail"
SPF = f"v=spf1 a:dev.{DOMAIN} ~all"
# From the Resend dashboard (domain sharetoken.app), 2026-11-10.
RESEND_DKIM = ("p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDiP9supaS4DSyt/WG+bY4vanbvofdFNv2BGhrPdQIkHIC0k2JHUrFNi194GaZKnTZ1SljO+xV6pZeWF869M2GvY2xzNAw/"
               "EV+I9wLR0qbYsVDHz7PtdXMcGgrBECs6nh6nC8h+3I89i8JEzxe0f/gjS7VwEbCLU+x3yZlkESQrNwIDAQAB")
RESEND_CNAMES = {"send": "send.forge.rmta.net", "rsend": "rsend.forge.rmta.net"}
API = f"https://api.godaddy.com/v1/domains/{DOMAIN}/records"


def token():
    if os.environ.get("GODADDY_ACCESS_TOKEN"):
        return os.environ["GODADDY_ACCESS_TOKEN"]
    env = pathlib.Path(__file__).resolve().parent.parent / "config.env"
    m = re.search(r"^GODADDY_ACCESS_TOKEN=(.*)$", env.read_text(), re.M)
    if not m:
        sys.exit("GODADDY_ACCESS_TOKEN not set (config.env)")
    return m.group(1).strip().strip("'\"")


def call(method, path, body=None):
    tok = token()
    auth = f"sso-key {tok}" if ":" in tok else f"Bearer {tok}"
    req = urllib.request.Request(API + path, method=method, headers={"Authorization": auth, "Content-Type": "application/json", "User-Agent": "sharetoken-mail-dns"},
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            data = r.read()
            return json.loads(data) if data else None
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path}: HTTP {e.code}: {e.read().decode()[:300]}")


def dkim_value(pem):
    der = subprocess.run(["openssl", "pkey", "-in", pem, "-pubout", "-outform", "DER"], check=True, capture_output=True).stdout
    return "v=DKIM1; k=rsa; p=" + base64.b64encode(der).decode()


def apply(dry, typ, name, want):
    cur = call("GET", f"/{typ}/{name}") or []
    have = sorted((r["data"], r.get("priority")) for r in cur)
    if have == sorted((r["data"], r.get("priority")) for r in want):
        print(f"ok      {typ:5} {name}")
        return
    short = lambda rs: [str(r["data"])[:70] for r in rs]
    print(f"{'would' if dry else 'write'}   {typ:5} {name}: {short(cur)} -> {short(want)}")
    if not dry:
        call("PUT", f"/{typ}/{name}", want)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-n", action="store_true", help="dry run")
    ap.add_argument("--dkim", action="append", default=[], metavar="SELECTOR=KEYFILE")
    a = ap.parse_args()
    ip = next(r["data"] for r in call("GET", "/A/@"))
    apply(a.n, "A", MAIL_HOST, [{"data": ip, "ttl": 600}])
    apply(a.n, "MX", "@", [{"data": f"{MAIL_HOST}.{DOMAIN}", "priority": 10, "ttl": 3600}])
    others = [r for r in (call("GET", "/TXT/@") or []) if not r["data"].lower().startswith("v=spf1")]
    apply(a.n, "TXT", "@", others + [{"data": SPF, "ttl": 3600}])
    apply(a.n, "TXT", "resend._domainkey", [{"data": RESEND_DKIM, "ttl": 3600}])
    for name, target in RESEND_CNAMES.items():
        apply(a.n, "CNAME", name, [{"data": target, "ttl": 3600}])
    for spec in a.dkim:
        sel, path = spec.split("=", 1)
        apply(a.n, "TXT", f"{sel}._domainkey", [{"data": dkim_value(path), "ttl": 3600}])


if __name__ == "__main__":
    main()
