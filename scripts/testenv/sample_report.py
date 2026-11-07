#!/usr/bin/env python3
"""Summarise sample.sh output: per process, RSS / fds at start, peak and end,
and the RSS slope over the run (KB per hour; a steady climb suggests a leak).

usage: sample_report.py samples.csv [since-epoch]
"""
import collections
import sys

since = float(sys.argv[2]) if len(sys.argv) > 2 else 0
rows = collections.defaultdict(list)
for line in open(sys.argv[1]):
    p = line.strip().split(",")
    if len(p) != 6 or not p[3].isdigit():
        continue
    t = float(p[0])
    if t < since:
        continue
    rows[(p[1], p[2])].append((t, int(p[3]), int(p[5]) if p[5].isdigit() else 0))

print("%-22s %-12s %5s %9s %9s %9s %10s %5s %5s" % ("host", "process", "n", "rss0 MB", "peak MB", "end MB", "MB/hour", "fd0", "fdEnd"))
for (host, proc), xs in sorted(rows.items()):
    xs.sort()
    n = len(xs)
    # least-squares slope of rss over time
    mt = sum(x[0] for x in xs) / n
    mr = sum(x[1] for x in xs) / n
    var = sum((x[0] - mt) ** 2 for x in xs)
    slope = (sum((x[0] - mt) * (x[1] - mr) for x in xs) / var * 3600 / 1024) if var else 0
    print("%-22s %-12s %5d %9.1f %9.1f %9.1f %10.1f %5d %5d" % (
        host[:22], proc, n, xs[0][1] / 1024, max(x[1] for x in xs) / 1024, xs[-1][1] / 1024, slope, xs[0][2], xs[-1][2]))
