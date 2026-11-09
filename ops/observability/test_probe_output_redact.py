#!/usr/bin/env python3
"""Tests for ops/observability/probe_output_redact.py. stdlib-only."""
from __future__ import annotations

import pathlib
import subprocess
import sys
import time
import unittest

_DIR = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_DIR))

import probe_output_redact as redact  # noqa: E402

_SCRIPT = _DIR / "probe_output_redact.py"
# Built at runtime so this file never contains a literal credential shape.
_STS = "IQoJ" + "b3JpZ2luX2VjE" + "A" * 120
_AKID = "ASIA" + "Q" * 16


class RedactLineTest(unittest.TestCase):
    def assertRedacted(self, line: str, secret: str) -> None:
        out, count = redact.redact_line(line)
        self.assertNotIn(secret, out)
        self.assertIn(redact.MASK, out)
        self.assertGreater(count, 0)

    def assertUnchanged(self, line: str) -> None:
        self.assertEqual(redact.redact_line(line), (line, 0))

    def test_pgbackrest_sts_token_regression(self) -> None:
        # 2026-11-08: repo1-s3-token leaked through a filter that forgot "token".
        self.assertRedacted(f"repo1-s3-token={_STS}", _STS)
        self.assertRedacted(f"repo1-s3-key={_AKID}", _AKID)
        self.assertRedacted("repo2-s3-key-secret=abcdEFGH1234ijkl5678", "abcdEFGH1234ijkl5678")

    def test_env_json_and_yaml_pairs(self) -> None:
        self.assertRedacted("DATABASE_PASSWORD=Sup3rSecretValue", "Sup3rSecretValue")
        self.assertRedacted('{"access_token": "opaque-value-0123456789"}', "opaque-value-0123456789")
        self.assertRedacted("  client_secret: 'zzzzYYYYxxxx'", "zzzzYYYYxxxx")
        self.assertRedacted("FEISHU_WEBHOOK_SECRET=abcdefgh12345678", "abcdefgh12345678")
        out, _ = redact.redact_line('{"refresh_token":"rt-0123456789abcdef","model":"gpt-6"}')
        self.assertIn('"model":"gpt-6"', out)

    def test_well_known_formats_without_names(self) -> None:
        jwt = "eyJ" + "a" * 20 + ".eyJ" + "b" * 20 + "." + "c" * 20
        for secret in (
            jwt,
            "sk-ant-" + "x" * 40,
            "ghp_" + "y" * 36,
            "ya29." + "z" * 40,
            _AKID,
        ):
            with self.subTest(secret=secret[:8]):
                self.assertRedacted(f"value seen: {secret} end", secret)
        self.assertRedacted("Authorization: Bearer abcdefghijklmnop", "abcdefghijklmnop")
        self.assertRedacted("postgres://tokenkey:hunter2pass@db:5432/x", "hunter2pass")
        self.assertRedacted("-----BEGIN OPENSSH PRIVATE KEY-----", "BEGIN OPENSSH PRIVATE KEY")

    def test_usage_and_metadata_fields_survive(self) -> None:
        for line in (
            "input_tokens=35612 output_tokens=1400",
            '{"input_tokens": 123, "cache_read_tokens": 0}',
            "api_key_id=5 user_id=2",
            "token_hash=3f2a9c0d1e4b5a6c7d8e9f00",
            "secret_configured=true",
            "password: [REDACTED]",
            "max_tokens: 64000",
            "tokenkey-green ghcr.io/youxuanxue/sub2api:1.8.356 Up 16 minutes (healthy)",
            "/var/lib/tokenkey/pgbackrest-conf/pgbackrest.conf",
            "token=short",
        ):
            with self.subTest(line=line):
                self.assertUnchanged(line)

    def test_long_line_stays_fast(self) -> None:
        line = "x" * 200_000 + " " + ("tokenkey_" * 20_000)
        started = time.monotonic()
        redact.redact_line(line)
        self.assertLess(time.monotonic() - started, 2.0)


class RedactWrapperTest(unittest.TestCase):
    def _run(self, body: str) -> subprocess.CompletedProcess[bytes]:
        return subprocess.run(
            [sys.executable, str(_SCRIPT), "--", "bash", "-c", body],
            capture_output=True,
            check=False,
        )

    def test_filters_both_streams_and_keeps_exit_code(self) -> None:
        proc = self._run(f'echo "repo1-s3-token={_STS}"; echo "PASSWORD=abcdefgh123" >&2; exit 7')
        self.assertEqual(proc.returncode, 7)
        self.assertNotIn(_STS.encode(), proc.stdout)
        self.assertIn(b"repo1-s3-token=[REDACTED]", proc.stdout)
        self.assertNotIn(b"abcdefgh123", proc.stderr)
        self.assertIn(b"[probe-redact] redacted 2 credential value(s)", proc.stderr)

    def test_clean_output_is_byte_identical(self) -> None:
        body = "printf 'a\\tb\\n{\"input_tokens\": 5}\\nno-trailing-newline'"
        proc = self._run(body)
        self.assertEqual(proc.returncode, 0)
        self.assertEqual(proc.stdout, b'a\tb\n{"input_tokens": 5}\nno-trailing-newline')
        self.assertEqual(proc.stderr, b"")

    def test_non_utf8_bytes_round_trip(self) -> None:
        proc = self._run("printf '\\xff\\xfe ok\\n'")
        self.assertEqual(proc.stdout, b"\xff\xfe ok\n")

    def test_requires_command(self) -> None:
        proc = subprocess.run([sys.executable, str(_SCRIPT)], capture_output=True, check=False)
        self.assertEqual(proc.returncode, 2)


if __name__ == "__main__":
    unittest.main()
