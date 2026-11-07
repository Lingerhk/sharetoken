#!/usr/bin/env python3
"""Summarise a chaos.sh run: per-buyer totals, a timeline in WINDOW-second
buckets aligned with the fault events, and the errors seen after each event.

usage: chaos_report.py <results/chaos dir> [window seconds, default 30]
"""
import collections
import glob
import os
import sys

d = sys.argv[1]
W = int(sys.argv[2]) if len(sys.argv) > 2 else 30
events = []
verifies = []
for line in open(os.path.join(d, "events.log")):
    t, what = line.split(" ", 1)
    if what.strip().startswith("verify:"):
        verifies.append(what.strip())
        continue
    events.append((float(t), what.strip()))
t0 = events[0][0]

logs = {}
for f in sorted(glob.glob(os.path.join(d, "*.log"))):
    name = os.path.basename(f)[:-4]
    if name == "events":
        continue
    rows = []
    for line in open(f):
        p = line.rstrip("\n").split(" ", 3)
        if len(p) >= 3:
            try:
                rows.append((float(p[0]), float(p[1]), p[2], p[3] if len(p) > 3 else ""))
            except ValueError:
                pass
    logs[name] = rows


def pct(xs, p):
    if not xs:
        return float("nan")
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round((len(xs) - 1) * p / 100.0)))]


if verifies:
    print("== restart checks: " + "; ".join(v[len("verify: "):] for v in verifies))
print("== totals")
for name, rows in logs.items():
    ok = [r for r in rows if r[2] == "200"]
    codes = collections.Counter(r[2] for r in rows if r[2] != "200")
    print("%-6s %5d requests  %6.2f%% ok  p50 %.2fs  p99 %.2fs  max %.1fs  errors %s" % (
        name, len(rows), 100.0 * len(ok) / max(1, len(rows)), pct([r[1] for r in ok], 50),
        pct([r[1] for r in ok], 99), max([r[1] for r in rows] or [0]), dict(codes)))

end = max([r[0] for rows in logs.values() for r in rows] + [events[-1][0]])
print("\n== timeline (%ds buckets; per buyer: requests ok%% p50/max latency of requests STARTED in the bucket)" % W)
print("%-7s %-34s " % ("t", "event") + " ".join("%-24s" % n for n in logs))
for b in range(0, int(end - t0) + W, W):
    lo, hi = t0 + b, t0 + b + W
    ev = "; ".join(w for t, w in events if lo <= t < hi)
    cells = []
    for name, rows in logs.items():
        rs = [r for r in rows if lo <= r[0] < hi]
        if not rs:
            cells.append("%-24s" % "-")
            continue
        ok = [r for r in rs if r[2] == "200"]
        bad = collections.Counter(r[2] for r in rs if r[2] != "200")
        c = "%3d %5.1f%% %.1f/%.0fs" % (len(rs), 100.0 * len(ok) / len(rs), pct([r[1] for r in ok], 50), max(r[1] for r in rs))
        if bad:
            c += " " + ",".join("%s:%d" % kv for kv in bad.most_common(2))
        cells.append("%-24s" % c)
    print("%-7s %-34s " % ("+%ds" % b, ev[:34]) + " ".join(cells))

print("\n== failed requests within 120s after each event")
for t, what in events[1:-1]:
    bad = collections.Counter()
    for name, rows in logs.items():
        for r in rows:
            if t <= r[0] < t + 120 and r[2] != "200":
                bad[name + ":" + r[2]] += 1
    print("+%-5d %-24s %s" % (t - t0, what, dict(bad) or "none"))

print("\n== most common error messages")
msgs = collections.Counter()
for name, rows in logs.items():
    for r in rows:
        if r[2] != "200":
            msgs[(name, r[2], r[3][:110])] += 1
for (name, code, msg), n in msgs.most_common(12):
    print("%6d  %-6s %-16s %s" % (n, name, code, msg))
