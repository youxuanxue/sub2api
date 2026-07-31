#!/usr/bin/env python3
"""Tests for TOKENKEY_IMAGE whole-line bump in ops/stage0/deploy_via_ssm.sh.

The legacy sed only rewrote `sub2api:<tag>` inside the value, so ad-hoc edge
experiments that pointed TOKENKEY_IMAGE at a local-only image (e.g.
tokenkey-antigravity-rt-fallback:1.8.130-local on us6) were never corrected
on deploy and caused `docker compose pull` to fail.

The script now replaces the entire TOKENKEY_IMAGE= line: canonical ghcr.io
*/sub2api repos keep their registry owner and only bump the tag; any other
value is reset to ghcr.io/youxuanxue/sub2api:<tag> with a drift warning.

Uses STAGE0_RENDER_ONLY to assert the rendered SSM commands; on Linux also
executes the bump block against a fake .env (GNU sed).
"""
from __future__ import annotations

import json
import os
import pathlib
import platform
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "deploy_via_ssm.sh"
_EDGE_IID = "mi-0edge000000000000"
_CANONICAL = "ghcr.io/youxuanxue/sub2api"


def _render(tag: str = "1.8.131", instance_id: str = _EDGE_IID):
    out_dir = tempfile.mkdtemp(prefix="tk-image-render-")
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env={
            **os.environ,
            "STAGE0_RENDER_ONLY": "1",
            "STAGE0_SSM_OUTPUT_DIR": out_dir,
            "EDGE_ID": "us6",
        },
        capture_output=True,
        text=True,
        check=False,
    )
    params = json.loads((pathlib.Path(out_dir) / "ssm-params.json").read_text())
    return proc, params["commands"]


def _bump_cmds(commands: list[str]) -> list[str]:
    out: list[str] = []
    capture = False
    for cmd in commands:
        if cmd.startswith("CUR=$(sed -n "):
            capture = True
        if capture:
            out.append(cmd)
        if capture and "grep" in cmd and "TOKENKEY_IMAGE" in cmd and "/var/lib/tokenkey/.env" in cmd:
            break
    return out


class TokenkeyImageBumpRenderTest(unittest.TestCase):
    def test_render_replaces_whole_line_not_sub2api_substring(self) -> None:
        proc, commands = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        joined = "\n".join(commands)
        self.assertNotIn("sub2api:[^[:space:]]*", joined)
        self.assertIn("s|^TOKENKEY_IMAGE=.*|TOKENKEY_IMAGE=${NEW}|", joined)
        self.assertIn("TOKENKEY_IMAGE drift", joined)
        self.assertIn("^ghcr.io/[^/]+/sub2api:", joined)

    def test_bump_block_has_five_commands(self) -> None:
        _, commands = _render()
        bump = _bump_cmds(commands)
        self.assertEqual(len(bump), 5, msg=bump)


@unittest.skipUnless(platform.system() == "Linux", "remote host is Linux (GNU sed)")
class TokenkeyImageBumpExecuteTest(unittest.TestCase):
    def _run_bump(self, env_body: str, tag: str = "1.8.131") -> str:
        host = pathlib.Path(tempfile.mkdtemp(prefix="tk-image-exec-"))
        (host / ".env").write_text(env_body)
        _, commands = _render(tag=tag)
        script = "\n".join(_bump_cmds(commands))
        script = script.replace("/var/lib/tokenkey", str(host)).replace("sudo ", "")
        proc = subprocess.run(
            ["bash", "-e", "-c", script],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=f"stdout={proc.stdout}\nstderr={proc.stderr}")
        return (host / ".env").read_text()

    def test_canonical_ghcr_image_bumps_tag_only(self) -> None:
        out = self._run_bump(f"TOKENKEY_IMAGE={_CANONICAL}:1.8.130\n")
        self.assertIn(f"TOKENKEY_IMAGE={_CANONICAL}:1.8.131\n", out)

    def test_drift_local_image_resets_to_canonical(self) -> None:
        drift = "tokenkey-antigravity-rt-fallback:1.8.130-local"
        out = self._run_bump(f"TOKENKEY_IMAGE={drift}\n")
        self.assertIn(f"TOKENKEY_IMAGE={_CANONICAL}:1.8.131\n", out)
        self.assertNotIn("antigravity", out)

    def test_other_ghcr_owner_preserves_repo(self) -> None:
        repo = "ghcr.io/other-owner/sub2api"
        out = self._run_bump(f"TOKENKEY_IMAGE={repo}:9.9.9\n", tag="1.0.0")
        self.assertIn(f"TOKENKEY_IMAGE={repo}:1.0.0\n", out)


if __name__ == "__main__":
    unittest.main()
