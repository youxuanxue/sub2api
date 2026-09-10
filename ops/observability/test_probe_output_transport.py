import hashlib
import os
from pathlib import Path
import subprocess
import sys
import unittest

from ops.observability.probe_output_transport import TransportError, decode, encode


class ProbeOutputTransportTest(unittest.TestCase):
    def test_large_output_round_trips_without_losing_rows(self):
        data = b"TERMINAL_FACT " + b'{"success":10}\n' * 4000
        self.assertGreater(len(data), 24000)
        self.assertEqual(decode(encode(data)), data)

    def test_rejects_corruption_length_mismatch_and_truncation(self):
        frame = encode(b"fact\n" * 100)
        prefix, length, checksum, payload = frame.split()
        for invalid in (
            frame[:-8], frame + "\n--output truncated--", "--output truncated--",
            f"{prefix} {int(length) - 1} {checksum} {payload}",
            f"{prefix} {length} {hashlib.sha256(b'wrong').hexdigest()} {payload}",
            f"{prefix} 999999999 {checksum} {payload}",
            frame + " trailing-record",
        ):
            with self.subTest(invalid=invalid[:50]), self.assertRaises(TransportError):
                decode(invalid)

    def test_incompressible_output_fails_before_ssm_can_truncate(self):
        with self.assertRaises(TransportError):
            encode(os.urandom(24000))

    def test_child_failure_preserves_status_and_never_emits_success_frame(self):
        script = Path(__file__).with_name("probe_output_transport.py")
        result = subprocess.run(
            [sys.executable, str(script), "encode", "--", sys.executable, "-c", "print('partial'); raise SystemExit(7)"],
            capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, b"")

    def test_cli_encodes_exact_stdout_including_trailing_newlines(self):
        script = Path(__file__).with_name("probe_output_transport.py")
        result = subprocess.run(
            [sys.executable, str(script), "encode", "--", sys.executable, "-c", "print('fact\\n')"],
            capture_output=True, check=True,
        )
        self.assertEqual(decode(result.stdout.decode()), b"fact\n\n")
