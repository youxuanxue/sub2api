"""Guards for cn-prod single-account concurrency ladder probe wiring."""

from __future__ import annotations

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROBE = ROOT / "ops/observability/probe-cn-prod-single-account-ladder.sh"
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


if __name__ == "__main__":
    unittest.main()
