#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import sys
import tempfile
import unittest

_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import pack_entry_host_config as pack  # noqa: E402


def _seed_entry_root(root: pathlib.Path) -> None:
    caddy = root / "caddy"
    (caddy / "data" / "caddy").mkdir(parents=True)
    (caddy / "config").mkdir(parents=True)
    (root / "data" / "legal").mkdir(parents=True)
    (caddy / "Caddyfile").write_text("tokenkey.dev {\n  respond /health 200\n}\n", encoding="utf-8")
    (caddy / "data" / "caddy" / "certificate").write_text(
        "not-a-real-cert\n", encoding="utf-8"
    )
    (root / "data" / "legal" / "terms.txt").write_text("ok\n", encoding="utf-8")


class PackEntryHostConfigTest(unittest.TestCase):
    def test_inventory_lists_caddy_and_legal(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "tokenkey"
            _seed_entry_root(root)
            inv = pack.discover(root)
            self.assertTrue(inv["has_caddy_material"])
            paths = {e["path"] for e in inv["entries"]}
            self.assertIn("caddy/Caddyfile", paths)
            self.assertIn("caddy/data", paths)
            self.assertIn("data/legal", paths)

    def test_inventory_empty_root_has_no_caddy(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "empty"
            root.mkdir()
            inv = pack.discover(root)
            self.assertFalse(inv["has_caddy_material"])

    def test_pack_writes_tar_gz_plaintext_and_plan_skips_apply(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "tokenkey"
            _seed_entry_root(root)
            out = pathlib.Path(tmp) / "prod-entry.tar.gz"
            result = pack.pack(root, out, site="prod")
            self.assertTrue(out.is_file())
            self.assertEqual(out.stat().st_mode & 0o777, 0o600)
            self.assertGreater(out.stat().st_size, 32)
            # gzip magic
            self.assertEqual(out.read_bytes()[:2], b"\x1f\x8b")
            self.assertEqual(result["format"], "tar.gz")
            self.assertEqual(result["encryption"], "none-operator")
            self.assertEqual(result["label"], "prod-entry")
            self.assertIn("upload_host_config.py", result["upload_hint"])
            self.assertNotIn("--apply", result["upload_hint"].split("add --apply")[0])

            planned = pack.plan_upload(root, site="prod")
            self.assertEqual(planned["upload_plan"]["operation"], "plan")
            self.assertIn("prod/host-config/prod-entry/", planned["upload_plan"]["uri"])

    def test_pack_refuses_without_caddy(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "empty"
            root.mkdir()
            with self.assertRaises(SystemExit) as raised:
                pack.pack(root, pathlib.Path(tmp) / "x.tar.gz", site="prod")
            self.assertIn("no caddy material", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
