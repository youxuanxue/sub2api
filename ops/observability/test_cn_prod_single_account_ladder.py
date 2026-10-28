"""Guards for cn-prod single-account concurrency ladder probe wiring."""

from __future__ import annotations

import base64
import json
import os
import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROBE = ROOT / "ops/observability/probe-cn-prod-single-account-ladder.sh"
FETCH = ROOT / "ops/observability/probe-fetch-file-chunk.sh"
PROBES_MD = (
    ROOT
    / ".cursor/skills/tokenkey-online-log-troubleshooting/references/probes.md"
)


class SingleAccountLadderProbeTest(unittest.TestCase):
    def test_account_id_must_be_numeric(self) -> None:
        text = PROBE.read_text(encoding="utf-8")
        self.assertIn('[[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]', text)
        self.assertIn("ACCOUNT_ID must be numeric", text)

    def test_docker_invocations_use_docker_bin_after_chooser(self) -> None:
        text = PROBE.read_text(encoding="utf-8")
        chooser = text.find("DOCKER_BIN=(")
        self.assertGreaterEqual(chooser, 0, "DOCKER_BIN chooser missing")
        after = text[chooser:]
        bare = re.findall(r"(?m)^(?!\s*#).*?\bdocker exec\b", after)
        self.assertEqual(
            bare,
            [],
            f"bare docker exec after DOCKER_BIN chooser: {bare!r}",
        )
        self.assertIn('"${DOCKER_BIN[@]}" exec', after)

    def test_cleanup_uses_probe_scope_not_hardcoded_newapi(self) -> None:
        text = PROBE.read_text(encoding="utf-8")
        self.assertNotRegex(
            text,
            r"tk_probe_clear_bindings\s+newapi\b",
            "cleanup must not hardcode newapi scope",
        )
        self.assertIn("TK_PROBE_SCOPE", text)
        self.assertIn("tk_probe_clear_bindings \"$scope\"", text)

    def test_no_dead_summarize_helper(self) -> None:
        text = PROBE.read_text(encoding="utf-8")
        self.assertNotIn("def summarize(", text)

    def test_skill_probe_table_wires_script(self) -> None:
        text = PROBES_MD.read_text(encoding="utf-8")
        self.assertIn("probe-cn-prod-single-account-ladder.sh", text)
        self.assertIn("probe_reserved_resources.sh", text)

    def test_public_entrance_approval_and_five_minute_hold(self) -> None:
        text = PROBE.read_text(encoding="utf-8")
        self.assertIn("https://api.galaxywaveai.com", text)
        self.assertIn("yes-formal-tpm-loadtest", text)
        self.assertIn('HOLD_S="${HOLD_S:-300}"', text)
        self.assertIn('STEPS="${STEPS:-5,10,15}"', text)
        self.assertIn('REQUEST_LOG="${REQUEST_LOG:-/tmp/tk-single-acct-ladder-${ACCOUNT_ID}.jsonl}"', text)
        self.assertIn("request_record_from_sample", text)
        self.assertIn("load_chat_case_pool", text)
        self.assertIn("prepare_replay_body", text)
        self.assertIn("synthetic_context_case", text)
        self.assertIn('export PROMPT_TOKENS_TARGET', text)
        self.assertIn('REQUEST_POOL="${REQUEST_POOL:-/tmp/tk-rt-cases-200-bodies.jsonl.gz}"', text)
        self.assertNotIn("pad_prompt", text)
        self.assertIn("tpm_burst_recipe1.py", text)
        self.assertIn("sys.modules[_spec.name] = _metrics", text)
        self.assertIn("ProxyHandler({})", text)
        self.assertNotIn("urllib.request.urlopen", text)
        self.assertNotIn('BASE_URL="${BASE_URL:-$APP_LOOP_URL}"', text)
        self.assertIn("transport_error = str(e)[:300]", text)
        self.assertNotIn("type(e).__name__", text)


class FetchLadderChunkTest(unittest.TestCase):
    def _run(self, **env: str) -> subprocess.CompletedProcess[str]:
        merged = os.environ.copy()
        merged.update(env)
        return subprocess.run(
            ["bash", str(FETCH)],
            env=merged,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_refuses_parent_segments(self) -> None:
        proc = self._run(FILE="/tmp/tk-single-acct-ladder-../../etc/passwd.jsonl", OFFSET="0", MAX="32")
        self.assertEqual(proc.returncode, 2)
        self.assertNotIn("root:", proc.stdout)

    def test_refuses_symlink_outside_tmp(self) -> None:
        link = "/tmp/tk-single-acct-ladder-review2965-link.jsonl"
        try:
            os.symlink("/etc/passwd", link)
            proc = self._run(FILE=link, OFFSET="0", MAX="32")
        finally:
            if os.path.islink(link):
                os.remove(link)
        self.assertEqual(proc.returncode, 2)
        self.assertNotIn("root:", proc.stdout)

    def test_reads_requested_slice(self) -> None:
        path = "/tmp/tk-single-acct-ladder-review2965.jsonl"
        try:
            with open(path, "wb") as handle:
                handle.write(b"abcdef")
            proc = self._run(FILE=path, OFFSET="2", MAX="3")
        finally:
            if os.path.exists(path):
                os.remove(path)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        body = json.loads(proc.stdout)
        self.assertEqual(body["offset"], 2)
        self.assertEqual(body["n"], 3)
        self.assertEqual(base64.b64decode(body["b64"]), b"cde")


if __name__ == "__main__":
    unittest.main()
