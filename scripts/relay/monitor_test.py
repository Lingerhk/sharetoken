#!/usr/bin/env python3
"""monitor.py 的测试：python3 scripts/relay/monitor_test.py（只用标准库，3.6 也能跑）。"""
import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.parse
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import monitor  # noqa: E402

HEALTHY = """# HELP x y
sharetoken_relay_info{peer_id="12D3"} 1
sharetoken_relay_service_enabled 1
sharetoken_relay_connections 7
sharetoken_relay_max_connections 8192
sharetoken_relay_reservations 2
sharetoken_relay_max_reservations 4096
sharetoken_relay_circuits 1
sharetoken_relay_relayed_bytes_total 5e+06
sharetoken_relay_reservations_denied_total 100
sharetoken_relay_circuits_denied_total 0
sharetoken_relay_passes_rejected_total 0
sharetoken_relay_network_coordinators_expected{network="prod"} 1
sharetoken_relay_network_coordinators_reserved{network="prod"} 1
sharetoken_relay_network_coordinators_expected{network="dev"} 1
sharetoken_relay_network_coordinators_reserved{network="dev"} 1
sharetoken_relay_network_reservations{network="dev"} 1
sharetoken_relay_network_max_reservations{network="dev"} 32
sharetoken_relay_network_max_reservations{network="prod"} 0
process_open_fds 20
process_max_fds 1024
process_cpu_seconds_total 10
process_start_time_seconds 1000
"""


def replace(text, name, val):
    return "\n".join(name + " " + str(val) if line.startswith(name + " ") else line for line in text.splitlines()) + "\n"


class Fake(object):
    """假中继 + 假 Pushover，同一个 HTTP 服务。"""

    def __init__(self):
        self.metrics = HEALTHY
        self.status = 200
        self.pushes = []
        self.push_status = 200
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(fake.status)
                self.end_headers()
                self.wfile.write(fake.metrics.encode())

            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"])).decode()
                if fake.push_status == 200:
                    fake.pushes.append({k: v[0] for k, v in urllib.parse.parse_qs(body).items()})
                self.send_response(fake.push_status)
                self.end_headers()
                self.wfile.write(json.dumps({"status": 0, "errors": ["bad"]}).encode())

            def log_message(self, *args):
                pass

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class Base(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        self.fake = Fake()
        self.addCleanup(self.fake.close)
        self.ini = os.path.join(self.dir, "monitor.ini")
        with open(self.ini, "w", encoding="utf-8") as f:
            f.write("[monitor]\nname = relay-t\nurl = http://127.0.0.1:%d/metrics\npushover_url = http://127.0.0.1:%d/push\n"
                    "disk_used_pct = 101\nmemory_available_pct = 0\n" % (self.fake.port, self.fake.port))
        with open(os.path.join(self.dir, "pushover.env"), "w", encoding="utf-8") as f:
            f.write("# comment\nPUSHOVER_TOKEN=tok\nPUSHOVER_USER='usr'\n")
        self.cfg = monitor.load_config(self.ini)

    def rounds(self, n=1):
        for _ in range(n):
            self.assertEqual(monitor.run(self.cfg), 0)

    def state(self):
        with open(self.cfg["state_file"], encoding="utf-8") as f:
            return json.load(f)


class TestAlerts(Base):
    def test_healthy_is_silent(self):
        self.rounds(5)
        self.assertEqual(self.fake.pushes, [])

    def test_alert_after_threshold_then_recovery_once(self):
        self.fake.metrics = replace(HEALTHY, 'sharetoken_relay_network_coordinators_reserved{network="prod"}', 0)
        self.rounds(2)
        self.assertEqual(self.fake.pushes, [], "a blip below fail_after must not alert")
        self.rounds(1)
        self.assertEqual(len(self.fake.pushes), 1)
        p = self.fake.pushes[0]
        self.assertIn("coordinator/prod", p["message"])
        self.assertEqual(p["priority"], "1")
        self.assertEqual(p["token"], "tok")
        self.assertEqual(p["user"], "usr")
        self.rounds(3)
        self.assertEqual(len(self.fake.pushes), 1, "no repeat inside repeat_minutes")
        self.fake.metrics = HEALTHY
        self.rounds(2)
        self.assertEqual(len(self.fake.pushes), 2, "one recovery message")
        self.assertIn("恢复 coordinator/prod", self.fake.pushes[1]["message"])
        self.assertEqual(self.fake.pushes[1]["priority"], "0", "a recovery must not be silent")

    def test_blip_never_alerted_sends_no_recovery(self):
        self.fake.metrics = replace(HEALTHY, "sharetoken_relay_service_enabled", 0)
        self.rounds(2)
        self.fake.metrics = HEALTHY
        self.rounds(2)
        self.assertEqual(self.fake.pushes, [])

    def test_dev_network_is_only_a_warning(self):
        self.fake.metrics = replace(HEALTHY, 'sharetoken_relay_network_coordinators_reserved{network="dev"}', 0)
        self.rounds(3)
        self.assertEqual(self.fake.pushes[0]["priority"], "0")

    def test_repeat_after_interval(self):
        self.fake.metrics = replace(HEALTHY, "sharetoken_relay_service_enabled", 0)
        self.rounds(3)
        st = self.state()
        st["checks"]["service"]["last"] -= 3601
        monitor.save_state(self.cfg, st)
        self.rounds(1)
        self.assertEqual(len(self.fake.pushes), 2)

    def test_relay_down_alerts_once_and_keeps_dependent_checks(self):
        self.fake.metrics = replace(HEALTHY, 'sharetoken_relay_network_coordinators_reserved{network="prod"}', 0)
        self.rounds(3)
        self.assertEqual(len(self.fake.pushes), 1)
        self.fake.status = 503
        self.rounds(3)
        self.assertEqual(len(self.fake.pushes), 2)
        self.assertIn("metrics", self.fake.pushes[1]["message"])
        self.assertNotIn("coordinator", self.fake.pushes[1]["message"], "metric checks did not run")
        self.assertIn("coordinator/prod", self.state()["checks"], "state kept while the relay is unreadable")

    def test_old_relay_without_metrics_is_a_failure(self):
        self.fake.metrics = '{"peers":1}'
        self.rounds(3)
        self.assertIn("没有中继指标", self.fake.pushes[0]["message"])

    def test_push_failure_retries_next_round(self):
        self.fake.metrics = replace(HEALTHY, "sharetoken_relay_service_enabled", 0)
        self.fake.push_status = 500
        self.rounds(4)
        self.assertEqual(self.fake.pushes, [])
        self.fake.push_status = 200
        self.rounds(1)
        self.assertEqual(len(self.fake.pushes), 1)
        self.assertTrue(self.state()["checks"]["service"]["alerted"])

    def test_recovery_survives_push_failure(self):
        self.fake.metrics = replace(HEALTHY, "sharetoken_relay_service_enabled", 0)
        self.rounds(3)
        self.fake.metrics = HEALTHY
        self.fake.push_status = 500
        self.rounds(2)
        self.fake.push_status = 200
        self.rounds(1)
        self.assertIn("恢复 service", self.fake.pushes[-1]["message"])

    def test_no_credentials_logs_only(self):
        os.remove(self.cfg["pushover_file"])
        self.fake.metrics = replace(HEALTHY, "sharetoken_relay_service_enabled", 0)
        self.rounds(3)
        self.assertEqual(self.fake.pushes, [])
        with open(self.cfg["log_file"], encoding="utf-8") as f:
            self.assertIn("未配置 Pushover", f.read())

    def test_secrets_never_logged(self):
        self.fake.metrics = replace(HEALTHY, "sharetoken_relay_service_enabled", 0)
        self.fake.push_status = 400
        self.rounds(3)
        with open(self.cfg["log_file"], encoding="utf-8") as f:
            log = f.read()
        self.assertIn("拒绝", log)
        self.assertNotIn("tok", log)
        self.assertNotIn("usr", log)

    def test_restart_notice_is_quiet(self):
        self.rounds(1)
        self.fake.metrics = replace(replace(HEALTHY, "process_start_time_seconds", 5000), "sharetoken_relay_reservations_denied_total", 0)
        self.rounds(1)
        self.assertEqual(len(self.fake.pushes), 1)
        self.assertIn("重启", self.fake.pushes[0]["message"])
        self.assertEqual(self.fake.pushes[0]["priority"], "-1")

    def test_corrupt_state_starts_over(self):
        with open(self.cfg["state_file"], "w") as f:
            f.write("{not json")
        self.rounds(1)
        self.assertEqual(self.state()["checks"], {})

    def test_removed_check_is_forgotten(self):
        st = {"checks": {"coordinator/gone": {"since": 1, "fails": 9, "alerted": True, "last": 1}}}
        monitor.save_state(self.cfg, st)
        self.rounds(1)
        self.assertEqual(self.state()["checks"], {})

    def test_long_message_truncated(self):
        ok = monitor.push(self.cfg, "t", "x" * 5000, 0)
        self.assertTrue(ok)
        self.assertEqual(len(self.fake.pushes[0]["message"]), monitor.PUSH_MAX_CHARS)

    def test_healthy_relay_stays_silent(self):
        self.rounds(3)
        self.assertEqual(self.fake.pushes, [])


class TestChecks(Base):
    def by_id(self, metrics_text, prev=None, elapsed=60):
        results, _ = monitor.check_relay(monitor.parse_metrics(metrics_text), self.cfg, prev or {}, elapsed)
        return {r.id: r for r in results}

    def test_healthy(self):
        for r in self.by_id(HEALTHY).values():
            self.assertTrue(r.ok, r.message)

    def test_capacity(self):
        r = self.by_id(replace(HEALTHY, "sharetoken_relay_reservations", 4000))
        self.assertFalse(r["capacity/reservations"].ok)
        r = self.by_id(replace(HEALTHY, 'sharetoken_relay_network_reservations{network="dev"}', 30))
        self.assertFalse(r["capacity/network/dev"].ok)
        self.assertNotIn("capacity/network/prod", r, "no cap, nothing to compare")

    def test_rates(self):
        prev = {"sharetoken_relay_reservations_denied_total": 0}
        self.assertFalse(self.by_id(HEALTHY, prev, 60)["rate/reservations_denied"].ok)
        self.assertTrue(self.by_id(HEALTHY, prev, 120)["rate/reservations_denied"].ok)

    def test_counter_reset_is_not_a_rate(self):
        prev = {"sharetoken_relay_reservations_denied_total": 9e9}
        self.assertTrue(self.by_id(HEALTHY, prev, 60)["rate/reservations_denied"].ok)

    def test_cpu(self):
        prev = {"process_cpu_seconds_total": 10 - 60 * (os.cpu_count() or 1)}
        self.assertFalse(self.by_id(HEALTHY, prev, 60)["cpu"].ok)

    def test_tcp(self):
        self.assertIsNone(monitor.check_tcp("127.0.0.1:%d" % self.fake.port))
        self.assertIsNotNone(monitor.check_tcp("127.0.0.1:1"))
        self.assertIsNotNone(monitor.check_tcp("noport"))
        self.cfg["tcp"] = "127.0.0.1:1"
        self.cfg["watch"] = "127.0.0.1:%d" % self.fake.port
        self.cfg["tcp_fail_after"] = "2"
        results = monitor.check_network(self.cfg)
        self.assertFalse(results[0].ok)
        self.assertEqual(results[0].fail_after, 2)
        self.assertTrue(results[1].ok)

    def test_parse_labels_and_values(self):
        m = monitor.parse_metrics('a{x="1",y="q\\"r"} 2.5e+00 1700\nb NaN\nbad line here\n')
        self.assertEqual(m["a"][0][0]["x"], "1")
        self.assertEqual(m["a"][0][1], 2.5)
        self.assertNotIn("bad", m)


class VersionTest(unittest.TestCase):
    def test_running_and_deployed(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        os.makedirs(os.path.join(d, "releases", "1.2.3"))
        open(os.path.join(d, "releases", "1.2.3", "relay"), "w").close()
        os.symlink(os.path.join("releases", "1.2.3", "relay"), os.path.join(d, "relay"))
        cfg = dict(monitor.DEFAULTS, base=d)
        self.assertEqual(monitor.deployed_version(cfg), "1.2.3")
        metrics = monitor.parse_metrics(HEALTHY + 'sharetoken_relay_build_info{version="1.2.2"} 1\n')
        self.assertEqual(monitor.version_of(metrics), "1.2.2")
        by_id = {r.id: r for r in monitor.check_relay(metrics, cfg, {}, 60)[0]}
        self.assertFalse(by_id["version"].ok)
        metrics = monitor.parse_metrics(HEALTHY + 'sharetoken_relay_build_info{version="1.2.3"} 1\n')
        self.assertTrue({r.id: r for r in monitor.check_relay(metrics, cfg, {}, 60)[0]}["version"].ok)


if __name__ == "__main__":
    unittest.main()
