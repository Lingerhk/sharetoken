#!/usr/bin/env python3
"""中继监控，由 cron 每分钟运行一次（docs/relay-monitor.md）。

读本机的 /metrics，探测 TCP 端口，看磁盘和内存；连续失败才告警，恢复时通知一次，
仍未恢复的每隔 repeat_minutes 重发一次。告警经 Pushover 发送，没配账号时只写日志。

    monitor.py -c monitor.ini            cron 用：检查、记状态、发通知
    monitor.py -c monitor.ini --check    只打印当前各项结果，不发通知、不写状态
    monitor.py -c monitor.ini --test     发一条测试通知

服务器上最老的是 CentOS 的 Python 3.6，cron 环境又常是 LANG=C：
只用标准库和 3.6 的语法，所有文本读写都显式指定 UTF-8。
"""
import argparse
import configparser
import fcntl
import json
import os
import re
import socket
import sys
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request

DEFAULTS = {
    "name": socket.gethostname(),
    "url": "http://127.0.0.1:8093/metrics",
    "tcp": "",                  # 本机对外端口（公网域名:端口，顺带验证 DNS），空格分隔
    "watch": "",                # 互相盯的其他中继，这台机器彻底宕机时只有它们能发现
    "watch_severity": "critical",
    "binary": "relay",          # 已部署的中继（软链到 releases/<版本>/relay）；相对于配置文件所在目录
    "warn_networks": "dev",     # 这些网络丢了协调器只算 warning
    "fail_after": "3",          # 连续失败多少次才告警（每分钟一次）
    "tcp_fail_after": "5",
    "repeat_minutes": "60",
    "capacity_pct": "80",       # 预约、连接、文件描述符占上限的比例
    "disk_used_pct": "90",
    "memory_available_pct": "10",
    "cpu_pct": "90",            # 中继进程 CPU，占全部核的比例
    "reservations_denied_per_min": "60",
    "circuits_denied_per_min": "60",
    "passes_rejected_per_min": "30",
    "pushover_file": "pushover.env",
    "pushover_url": "https://api.pushover.net/1/messages.json",
    "state_file": "monitor.state",
    "log_file": "monitor.log",
}
HTTP_TIMEOUT, TCP_TIMEOUT, PUSH_TIMEOUT = 5, 5, 15
LOG_MAX_BYTES = 1 << 20
PUSH_MAX_CHARS = 1024  # Pushover 单条消息上限
OUTBOX_MAX = 20
PRIORITY = {"critical": 1, "warning": 0}
LABEL = {"critical": "严重", "warning": "警告"}


class Result(object):
    """一项检查在这一轮的结果。"""

    def __init__(self, id_, ok, message, severity="critical", fail_after=None):
        self.id, self.ok, self.message, self.severity, self.fail_after = id_, ok, message, severity, fail_after


def load_config(path):
    parser = configparser.ConfigParser(interpolation=None, defaults=DEFAULTS)
    with open(path, encoding="utf-8") as f:
        parser.read_file(f)
    cfg = dict(parser["monitor"])
    base = os.path.dirname(os.path.abspath(path))
    for key in ("pushover_file", "state_file", "log_file"):
        cfg[key] = os.path.join(base, cfg[key])
    cfg["base"] = base
    return cfg


def parse_metrics(text):
    """把 Prometheus 文本格式解析成 {指标名: [(标签, 值)]}。"""
    out = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = re.match(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})?\s+(\S+)', line)
        if not m:
            continue
        try:
            value = float(m.group(3))
        except ValueError:
            continue
        labels = dict(re.findall(r'(\w+)="((?:[^"\\]|\\.)*)"', m.group(2) or ""))
        out.setdefault(m.group(1), []).append((labels, value))
    return out


def scrape(url):
    """返回 (指标, 错误)。没有 sharetoken_relay_* 也算失败：端口上不是新版中继。"""
    try:
        with urllib.request.urlopen(url, timeout=HTTP_TIMEOUT) as r:
            metrics = parse_metrics(r.read().decode("utf-8", "replace"))
    except Exception as e:  # 连不上、超时、HTTP 错误，都是一个结论
        return None, "%s 无响应：%s" % (url, e)
    if "sharetoken_relay_service_enabled" not in metrics:
        return None, "%s 没有中继指标（中继版本过旧？）" % url
    return metrics, None


def value(metrics, name, default=None):
    series = metrics.get(name)
    return series[0][1] if series else default


def by_network(metrics, name):
    return {labels.get("network", ""): v for labels, v in metrics.get(name, [])}


def version_of(metrics):
    """中继自己报告的版本；老版本没有这项指标时返回 None。"""
    series = metrics.get("sharetoken_relay_build_info")
    return series[0][0].get("version") if series else None


def deployed_version(cfg):
    """binary 软链指向 releases/<版本>/relay 时返回版本，否则 None。"""
    path = os.path.realpath(os.path.join(cfg.get("base", ""), cfg["binary"]))
    parts = path.split(os.sep)
    if len(parts) >= 3 and parts[-3] == "releases":
        return parts[-2]
    return None


def check_relay(metrics, cfg, prev_counters, elapsed):
    """从指标判断中继本身。返回 (结果, 这一轮的计数器)。"""
    results = []
    enabled = value(metrics, "sharetoken_relay_service_enabled", 0) == 1
    results.append(Result("service", enabled, "中继服务未运行，无法预约" if not enabled else "中继服务运行中"))

    running = version_of(metrics)
    deployed = deployed_version(cfg)
    if running and deployed:
        ok = running == deployed
        results.append(Result("version", ok, "正在运行 %s，但已部署 %s，重启没有生效" % (running, deployed) if not ok else "版本 %s" % running, "warning"))

    warn_networks = cfg["warn_networks"].split()
    reserved = by_network(metrics, "sharetoken_relay_network_coordinators_reserved")
    for net, expected in sorted(by_network(metrics, "sharetoken_relay_network_coordinators_expected").items()):
        if expected <= 0:
            continue
        ok = reserved.get(net, 0) >= 1
        results.append(Result("coordinator/" + net, ok,
                              "网络 %s 的协调器没有预约，该网络的用户经这台中继连不上协调器" % net if not ok
                              else "网络 %s 的协调器有预约" % net,
                              "warning" if net in warn_networks else "critical"))

    limit = float(cfg["capacity_pct"])

    def capacity(id_, label, used, maximum):
        if used is None or not maximum:
            return
        pct = 100.0 * used / maximum
        results.append(Result(id_, pct < limit, "%s %d/%d（%.0f%%）" % (label, used, maximum, pct), "warning"))

    capacity("capacity/reservations", "预约", value(metrics, "sharetoken_relay_reservations"),
             value(metrics, "sharetoken_relay_max_reservations"))
    capacity("capacity/connections", "连接", value(metrics, "sharetoken_relay_connections"),
             value(metrics, "sharetoken_relay_max_connections"))
    capacity("capacity/fds", "文件描述符", value(metrics, "process_open_fds"), value(metrics, "process_max_fds"))
    held = by_network(metrics, "sharetoken_relay_network_reservations")
    for net, maximum in sorted(by_network(metrics, "sharetoken_relay_network_max_reservations").items()):
        capacity("capacity/network/" + net, "网络 %s 预约" % net, held.get(net), maximum)

    counters = {}
    rates = (("rate/reservations_denied", "sharetoken_relay_reservations_denied_total", "预约被拒", "reservations_denied_per_min"),
             ("rate/circuits_denied", "sharetoken_relay_circuits_denied_total", "电路被拒", "circuits_denied_per_min"),
             ("rate/passes_rejected", "sharetoken_relay_passes_rejected_total", "通行证被拒", "passes_rejected_per_min"))

    def rate(name):
        """每秒增量。第一轮、重启后计数器归零、两轮隔得太近时算不出来。"""
        now_v, old_v = value(metrics, name), prev_counters.get(name)
        if now_v is not None:
            counters[name] = now_v
        if now_v is None or old_v is None or now_v < old_v or elapsed < 10:
            return None
        return (now_v - old_v) / elapsed

    for id_, name, label, key in rates:
        r = rate(name)
        per_min = 0.0 if r is None else r * 60
        results.append(Result(id_, per_min < float(cfg[key]), "%s %.0f 次/分钟" % (label, per_min), "warning"))
    r = rate("process_cpu_seconds_total")
    pct = 0.0 if r is None else 100.0 * r / (os.cpu_count() or 1)
    results.append(Result("cpu", pct < float(cfg["cpu_pct"]), "中继进程 CPU %.0f%%" % pct, "warning"))
    return results, counters


def check_tcp(addr):
    host, _, port = addr.rpartition(":")
    try:
        socket.create_connection((host.strip("[]"), int(port)), TCP_TIMEOUT).close()
        return None
    except (OSError, ValueError) as e:
        return str(e)


def check_network(cfg):
    results = []
    after = int(cfg["tcp_fail_after"])
    for key, severity, label in (("tcp", "critical", "本机端口"), ("watch", cfg["watch_severity"], "对端中继")):
        for addr in cfg[key].split():
            err = check_tcp(addr)
            results.append(Result(key + "/" + addr, err is None,
                                  "%s %s 连不上：%s" % (label, addr, err) if err else "%s %s 可连接" % (label, addr),
                                  severity, after))
    return results


def check_system(cfg):
    results = []
    st = os.statvfs(cfg["base"])
    used = 100.0 * (1 - float(st.f_bavail) / st.f_blocks) if st.f_blocks else 0.0
    results.append(Result("disk", used < float(cfg["disk_used_pct"]), "磁盘已用 %.0f%%" % used, "warning"))
    try:
        with open("/proc/meminfo", encoding="utf-8") as f:
            info = dict((k, int(v.split()[0])) for k, v in (line.split(":", 1) for line in f))
        avail = 100.0 * info["MemAvailable"] / info["MemTotal"]
        results.append(Result("memory", avail >= float(cfg["memory_available_pct"]), "可用内存 %.0f%%" % avail, "warning"))
    except (OSError, KeyError, ValueError, ZeroDivisionError):
        pass  # 没有 /proc/meminfo（在 Mac 上跑测试）
    return results


def minutes(seconds):
    return "%d 分钟" % max(1, round(seconds / 60.0))


def advance(state, results, complete, now, cfg):
    """推进每项检查的状态。返回这一轮要发的告警 [(检查 id, 严重程度, 文本)]，发送成功后才算已告警。

    complete 为假时（指标抓取失败）依赖指标的检查没有跑，它们的状态原样保留；
    为真时，不在 results 里的检查已不存在（配置改了），直接忘掉。
    """
    checks = state.setdefault("checks", {})
    outbox = state.setdefault("outbox", [])
    alerts = []
    seen = set()
    default_after, repeat = int(cfg["fail_after"]), int(cfg["repeat_minutes"]) * 60
    for r in results:
        seen.add(r.id)
        c = checks.get(r.id)
        if r.ok:
            if c and c.get("alerted"):
                outbox.append({"priority": 0, "text": "恢复 %s：%s（持续了 %s）" % (r.id, r.message, minutes(now - c["since"]))})
            checks.pop(r.id, None)
            continue
        if c is None:
            c = checks[r.id] = {"since": now, "fails": 0, "alerted": False, "last": 0}
        c["fails"] += 1
        if c["fails"] >= (r.fail_after or default_after) and (not c["alerted"] or now - c["last"] >= repeat):
            alerts.append((r.id, r.severity, "%s %s：%s（已持续 %s）" % (LABEL[r.severity], r.id, r.message, minutes(now - c["since"]))))
    if complete:
        for id_ in [i for i in checks if i not in seen]:
            del checks[id_]
    del outbox[:-OUTBOX_MAX]
    return alerts


def note_restart(state, metrics):
    started = value(metrics, "process_start_time_seconds")
    if started is None:
        return
    old = state.get("started")
    if old is not None and abs(started - old) > 1:
        state.setdefault("outbox", []).append({"priority": -1, "text": "通知：中继进程重启过，新进程启动于 %s UTC" % time.strftime("%m-%d %H:%M", time.gmtime(started))})
    state["started"] = started


def read_pushover(cfg):
    """pushover.env：PUSHOVER_TOKEN=… 和 PUSHOVER_USER=…。没有或不全时返回 None。"""
    creds = {}
    try:
        with open(cfg["pushover_file"], encoding="utf-8") as f:
            for line in f:
                key, sep, val = line.strip().partition("=")
                if sep and not key.startswith("#"):
                    creds[key.strip()] = val.strip().strip("\"'")
    except OSError:
        return None
    if creds.get("PUSHOVER_TOKEN") and creds.get("PUSHOVER_USER"):
        return creds
    return None


def push(cfg, title, message, priority):
    """发一条 Pushover。返回 True 表示可以视为已送达（含没配账号、只写日志的情况）。"""
    creds = read_pushover(cfg)
    if creds is None:
        log(cfg, "未配置 Pushover，只写日志：%s | %s" % (title, message.replace("\n", " / ")))
        return True
    if len(message) > PUSH_MAX_CHARS:
        message = message[:PUSH_MAX_CHARS - 1] + "…"
    body = urllib.parse.urlencode({"token": creds["PUSHOVER_TOKEN"], "user": creds["PUSHOVER_USER"], "title": title,
                                   "message": message, "priority": priority}).encode("utf-8")
    try:
        with urllib.request.urlopen(cfg["pushover_url"], data=body, timeout=PUSH_TIMEOUT) as r:
            r.read()
        log(cfg, "已发送 [%d] %s | %s" % (priority, title, message.replace("\n", " / ")))
        return True
    except urllib.error.HTTPError as e:
        try:
            detail = json.loads(e.read().decode("utf-8", "replace")).get("errors")
        except ValueError:
            detail = ""
        log(cfg, "Pushover 拒绝（HTTP %d）%s，下一轮重试：%s" % (e.code, detail, title))
    except Exception as e:  # 网络错误不能让这一轮丢状态
        log(cfg, "Pushover 发送失败（%s），下一轮重试：%s" % (e, title))
    return False


def log(cfg, text):
    path = cfg["log_file"]
    try:
        if os.path.getsize(path) > LOG_MAX_BYTES:
            os.replace(path, path + ".1")
    except OSError:
        pass
    with open(path, "a", encoding="utf-8") as f:
        f.write("%s %s\n" % (time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), text))


def load_state(cfg):
    try:
        with open(cfg["state_file"], encoding="utf-8") as f:
            state = json.load(f)
        return state if isinstance(state, dict) else {}
    except OSError:
        return {}
    except ValueError:
        log(cfg, "状态文件损坏，重新开始")
        return {}


def save_state(cfg, state):
    tmp = cfg["state_file"] + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(state, f, ensure_ascii=False)
    os.replace(tmp, cfg["state_file"])


def gather(cfg, state, now):
    """跑全部检查。返回 (结果, 指标, 计数器)；指标抓取失败时指标为 None。"""
    metrics, err = scrape(cfg["url"])
    results = [Result("metrics", err is None, err or "指标正常")]
    counters = {}
    if metrics is not None:
        elapsed = now - state.get("counters_at", now)
        relay_results, counters = check_relay(metrics, cfg, state.get("counters", {}), elapsed)
        results += relay_results
    results += check_network(cfg)
    results += check_system(cfg)
    return results, metrics, counters


def run(cfg):
    with open(cfg["state_file"] + ".lock", "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            return 0  # 上一轮还没跑完
        return run_locked(cfg)


def run_locked(cfg):
    state = load_state(cfg)
    now = time.time()
    results, metrics, counters = gather(cfg, state, now)
    if metrics is not None:
        state["counters"], state["counters_at"] = counters, now
        note_restart(state, metrics)
    alerts = advance(state, results, metrics is not None, now, cfg)

    outbox = state.get("outbox", [])
    if alerts or outbox:
        lines = [a[2] for a in alerts] + [o["text"] for o in outbox]
        priority = max([PRIORITY[a[1]] for a in alerts] + [o["priority"] for o in outbox])
        if alerts:
            title = "%s：%d 项异常" % (cfg["name"], len(alerts))
        else:
            title = "%s：状态变化" % cfg["name"]
        if push(cfg, title, "\n".join(lines), priority):
            for id_, _, _ in alerts:
                state["checks"][id_].update(alerted=True, last=now)
            state["outbox"] = []

    save_state(cfg, state)
    return 0


def check_only(cfg):
    state = load_state(cfg)
    results, _, _ = gather(cfg, state, time.time())
    bad = 0
    for r in results:
        bad += 0 if r.ok else 1
        write_out("%-4s %-34s %s\n" % ("ok" if r.ok else "FAIL", r.id, r.message))
    write_out("Pushover：%s\n" % ("已配置" if read_pushover(cfg) else "未配置，只写日志"))
    return 1 if bad else 0


def write_out(text):
    sys.stdout.buffer.write(text.encode("utf-8"))
    sys.stdout.buffer.flush()


def main():
    ap = argparse.ArgumentParser(description="sharetoken relay monitor")
    ap.add_argument("-c", "--config", required=True)
    ap.add_argument("--check", action="store_true", help="print each check; no notifications, no state")
    ap.add_argument("--test", action="store_true", help="send a test notification")
    args = ap.parse_args()
    cfg = load_config(args.config)
    if args.check:
        return check_only(cfg)
    if args.test:
        if read_pushover(cfg) is None:
            write_out("未配置 Pushover（%s）\n" % cfg["pushover_file"])
            return 1
        ok = push(cfg, "%s：测试通知" % cfg["name"], "监控通知正常。", 0)
        write_out("已发送\n" if ok else "发送失败，见 %s\n" % cfg["log_file"])
        return 0 if ok else 1
    try:
        return run(cfg)
    except Exception:
        log(cfg, "监控异常：\n" + traceback.format_exc())
        return 1


if __name__ == "__main__":
    sys.exit(main())
