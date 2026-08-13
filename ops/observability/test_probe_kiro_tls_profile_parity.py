#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "probe-kiro-tls-profile-parity.sh"


class ProbeKiroTLSProfileParityTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(["bash", "-n", str(SCRIPT)], capture_output=True, text=True, check=False)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_emits_only_canonical_tls_fields(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            fakebin = Path(td) / "bin"
            fakebin.mkdir()
            docker = fakebin / "docker"
            docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    case "$*" in
                      *"FROM tls_fingerprint_profiles"*"WHERE name = 'tk_canonical_kiro_cli'"*)
                        echo '{"name":"tk_canonical_kiro_cli","enable_grease":false,"shuffle_extensions":true,"cipher_suites":[1],"curves":[2],"point_formats":[0],"signature_algorithms":[3],"alpn_protocols":[],"supported_versions":[772],"key_share_groups":[29],"psk_modes":[1],"extensions":[0]}'
                        ;;
                      *) exit 9 ;;
                    esac
                    """
                ),
                encoding="utf-8",
            )
            docker.chmod(0o755)
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                env={**os.environ, "PATH": f"{fakebin}:{os.environ.get('PATH', '')}"},
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        row = json.loads(proc.stdout)
        self.assertEqual(row["name"], "tk_canonical_kiro_cli")
        self.assertTrue(row["shuffle_extensions"])
        self.assertNotIn("description", row)
        self.assertNotIn("credentials", row)
        self.assertNotIn("account_id", row)

    def test_empty_query_reports_missing(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            fakebin = Path(td) / "bin"
            fakebin.mkdir()
            docker = fakebin / "docker"
            docker.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
            docker.chmod(0o755)
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                env={**os.environ, "PATH": f"{fakebin}:{os.environ.get('PATH', '')}"},
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(json.loads(proc.stdout)["status"], "missing")


if __name__ == "__main__":
    unittest.main()
