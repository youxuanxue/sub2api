#!/usr/bin/env python3
"""Offline unit tests for proxy_bbr_live.py."""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
MOD_PATH = REPO / "scripts/checks/proxy_bbr_live.py"


def _load():
    spec = importlib.util.spec_from_file_location("proxy_bbr_live", MOD_PATH)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class ProxyBbrLiveTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.mod = _load()

    def test_parse_proxy_probe_stdout(self) -> None:
        text = """
=== proxies_active ===
{"id":2,"name":"jp-uk1","protocol":"socks5","host":"52.198.234.59","port":1080,"status":"active"}
{"id":3,"name":"jp-uk2","protocol":"socks5","host":"54.238.63.107","port":1080,"status":"active"}
not-json
"""
        got = self.mod.parse_proxy_probe_stdout(text)
        self.assertEqual([p["host"] for p in got], ["52.198.234.59", "54.238.63.107"])
        self.assertEqual(got[0]["name"], "jp-uk1")

    def test_map_hosts_to_lightsail_reports_missing(self) -> None:
        proxies = [
            {"id": 2, "name": "jp-uk1", "host": "1.2.3.4"},
            {"id": 3, "name": "jp-uk2", "host": "5.6.7.8"},
        ]
        instances = [{"name": "jp-proxy-01-micro", "public_ip": "1.2.3.4"}]
        mapping, errors = self.mod.map_hosts_to_lightsail(proxies, instances)
        self.assertEqual(mapping, {"1.2.3.4": "jp-proxy-01-micro"})
        self.assertEqual(len(errors), 1)
        self.assertIn("5.6.7.8", errors[0])

    def test_check_proxies_live_ok(self) -> None:
        proxies = [
            {"id": 2, "name": "jp-uk1", "host": "1.2.3.4"},
            {"id": 3, "name": "jp-uk2", "host": "5.6.7.8"},
        ]
        instances = [
            {"name": "a", "public_ip": "1.2.3.4"},
            {"name": "b", "public_ip": "5.6.7.8"},
        ]
        errors = self.mod.check_proxies_live(
            proxies=proxies,
            instances=instances,
            ssh_sysctl=lambda _name: ("bbr", "fq"),
        )
        self.assertEqual(errors, [])

    def test_check_proxies_live_detects_cubic(self) -> None:
        proxies = [{"id": 2, "name": "jp-uk1", "host": "1.2.3.4"}]
        instances = [{"name": "a", "public_ip": "1.2.3.4"}]
        errors = self.mod.check_proxies_live(
            proxies=proxies,
            instances=instances,
            ssh_sysctl=lambda _name: ("cubic", "fq_codel"),
        )
        self.assertEqual(len(errors), 1)
        self.assertIn("cc='cubic'", errors[0])
        self.assertIn("want cc='bbr'", errors[0])


if __name__ == "__main__":
    unittest.main()
