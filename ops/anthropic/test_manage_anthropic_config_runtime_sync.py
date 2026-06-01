#!/usr/bin/env python3
"""Unit tests for sync-runtime + guard-drift plan helpers in manage-anthropic-config."""
from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-anthropic-config.py"
_spec = importlib.util.spec_from_file_location("manage_anthropic_config", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(mgr)


class RuntimeSyncShellTest(unittest.TestCase):
    def test_render_runtime_sync_shell_uses_rediscli_without_auth_env(self) -> None:
        shell = mgr.render_runtime_sync_shell("2.1.152")
        self.assertIn("env -u REDISCLI_AUTH", shell)
        self.assertIn("claude_code_user_agent_version", shell)
        self.assertIn("claude_code_http_mimicry_manifest", shell)
        self.assertIn("fingerprint:${id}", shell)

    def test_render_runtime_sync_shell_persists_valid_json_manifest(self) -> None:
        # Regression: the manifest UPSERT must be delivered as base64 SQL piped to
        # psql, never inlined into `psql -c "..."`. Inlining stripped the JSON's
        # double-quotes against the -c "..." wrapper and persisted an invalid-JSON
        # value (unfixable http_ua_drift). Decode the blob and confirm the value
        # round-trips as JSON.
        import base64
        import re

        shell = mgr.render_runtime_sync_shell("2.1.159")
        self.assertIn("docker exec -i tokenkey-postgres", shell)  # stdin pipe needs -i
        lines = shell.splitlines()
        idx = next(i for i, l in enumerate(lines) if "settings_upsert_http_mimicry" in l)
        m = re.search(r"echo (\S+) \| base64 -d \| \$PSQL", lines[idx + 1])
        self.assertIsNotNone(m, "manifest must be base64-piped to psql, not inlined")
        sql = base64.b64decode(m.group(1)).decode("utf-8")
        json_blob = re.search(r"\{.*\}", sql)
        self.assertIsNotNone(json_blob)
        parsed = json.loads(json_blob.group(0))  # must NOT raise — guards the quote bug
        expected = json.loads(mgr._http_mimicry_manifest_json())
        self.assertEqual(parsed["cc_version"], expected["cc_version"])
        self.assertEqual(parsed["sonnet_opus"], expected["sonnet_opus"])
        self.assertEqual(parsed["haiku"], expected["haiku"])

    def test_http_mimicry_manifest_from_baseline(self) -> None:
        manifest = json.loads(mgr._http_mimicry_manifest_json())
        self.assertEqual(1, manifest["schema_version"])
        self.assertRegex(manifest["cc_version"], r"^\d+\.\d+\.\d+$")
        self.assertGreaterEqual(len(manifest["sonnet_opus"]), 1)
        self.assertGreaterEqual(len(manifest["haiku"]), 1)

    def test_plan_http_mimicry_sync_writes_plan(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp) / "plan.json"
            rc = mgr.cmd_plan_http_mimicry_sync(
                __import__("argparse").Namespace(out=str(out)),
            )
            self.assertEqual(rc, 0)
            plan = json.loads(out.read_text())
            self.assertEqual("http_mimicry_runtime_sync", plan["intent"]["kind"])
            self.assertEqual(1, len(plan["actions"]))

    def test_canonical_ua_version_from_repo_json(self) -> None:
        ver = mgr._canonical_claude_code_ua_version()
        self.assertRegex(ver, r"^\d+\.\d+\.\d+$")

    def test_canonical_ua_version_rejects_bad_override(self) -> None:
        with self.assertRaises(SystemExit):
            mgr._canonical_claude_code_ua_version("not-a-semver")


class GuardDriftPlanTest(unittest.TestCase):
    def test_iter_guard_drift_accounts_dedupes(self) -> None:
        report = {
            "guards": [{
                "report": {
                    "selector": {"edge_id": "uk1"},
                    "items": [
                        {
                            "status": "drift",
                            "account_name": "acct-a",
                            "baseline_tier": "l3",
                            "diffs": [{"path": "/tls_profile/description"}],
                        },
                        {
                            "status": "ok",
                            "account_name": "acct-b",
                            "baseline_tier": "l3",
                        },
                    ],
                },
            }],
        }
        items = mgr._iter_guard_drift_accounts(report)
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["edge_id"], "uk1")
        self.assertEqual(items[0]["account_name"], "acct-a")
        self.assertEqual(items[0]["tier"], "l3")

    def test_runtime_sync_targets_from_plan_includes_prod_and_edges(self) -> None:
        plan = {
            "actions": [
                {"kind": "edge_account_tier", "target": {"edge_id": "uk1"}},
                {"kind": "edge_account_tier", "target": {"edge_id": "us1"}},
            ],
        }
        targets = mgr._runtime_sync_targets_from_plan(plan, include_prod=True)
        self.assertEqual(targets, ["prod", "edge:uk1", "edge:us1"])

    def test_plan_guard_drift_fix_builds_force_rewrite_plan(self) -> None:
        baseline = mgr._load_tier_baselines()["l3"]
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            snap = {
                "version": mgr.SNAPSHOT_VERSION,
                "captured_at": "2026-05-27T00:00:00Z",
                "edges": {
                    "uk1": {
                        "deployable": True,
                        "oauth_accounts": [{
                            "id": 1,
                            "name": "acct-a",
                            "stability_tier": "l3",
                            **{k: baseline[k] for k in mgr._TIER_BASELINE_FIELDS},
                        }],
                    },
                },
            }
            snap_path = tmp_path / "snap.json"
            snap_path.write_text(json.dumps(snap))
            check_path = tmp_path / "check.json"
            check_path.write_text(json.dumps({
                "guards": [{
                    "report": {
                        "selector": {"edge_id": "uk1"},
                        "items": [{
                            "status": "drift",
                            "account_name": "acct-a",
                            "baseline_tier": "l3",
                            "diffs": [{"path": "/tls_profile/description"}],
                        }],
                    },
                }],
            }))
            plan_path = tmp_path / "plan.json"
            rc = mgr.cmd_plan_guard_drift_fix(
                __import__("argparse").Namespace(
                    snapshot=str(snap_path),
                    check_report=str(check_path),
                    allow_planned=False,
                    out=str(plan_path),
                )
            )
            self.assertEqual(rc, 0)
            plan = json.loads(plan_path.read_text())
            self.assertFalse(plan["noop"])
            self.assertEqual(len(plan["actions"]), 1)
            self.assertTrue(plan["intent"]["force_template_rewrite"])


class HttpUaDriftTest(unittest.TestCase):
    """Live HTTP UA / mimicry-manifest drift comparison (the check blind spot
    that let the fleet run a stale cc UA while `check` stayed green)."""

    EXPECTED = {
        "cc_version": "2.1.159",
        "sonnet_opus": ["claude-code-20250219", "oauth-2025-04-20"],
        "haiku": ["oauth-2025-04-20"],
    }

    def _manifest(self, **over) -> str:
        m = {
            "schema_version": 1,
            "cc_version": self.EXPECTED["cc_version"],
            "sonnet_opus": list(self.EXPECTED["sonnet_opus"]),
            "haiku": list(self.EXPECTED["haiku"]),
        }
        m.update(over)
        return json.dumps(m, separators=(",", ":"))

    def test_in_sync_no_drift(self) -> None:
        live = {
            "claude_code_user_agent_version": "2.1.159",
            "claude_code_http_mimicry_manifest": self._manifest(),
        }
        self.assertEqual([], mgr._http_ua_drift_items("edge:us1", live, self.EXPECTED))

    def test_stale_ua_version_is_drift(self) -> None:
        live = {
            "claude_code_user_agent_version": "2.1.158",  # stale — the real incident
            "claude_code_http_mimicry_manifest": self._manifest(),
        }
        items = mgr._http_ua_drift_items("edge:us1", live, self.EXPECTED)
        self.assertEqual(1, len(items))
        self.assertEqual("drift", items[0]["status"])
        self.assertEqual("claude_code_user_agent_version", items[0]["field"])
        self.assertEqual("2.1.158", items[0]["actual"])

    def test_unset_ua_is_drift(self) -> None:
        items = mgr._http_ua_drift_items("prod", {}, self.EXPECTED)
        # both UA and manifest unset -> two drift items
        fields = {i["field"] for i in items}
        self.assertEqual(
            {"claude_code_user_agent_version", "claude_code_http_mimicry_manifest"}, fields
        )
        self.assertTrue(all(i["status"] == "drift" for i in items))

    def test_manifest_cc_version_drift(self) -> None:
        live = {
            "claude_code_user_agent_version": "2.1.159",
            "claude_code_http_mimicry_manifest": self._manifest(cc_version="2.1.158"),
        }
        items = mgr._http_ua_drift_items("edge:us1", live, self.EXPECTED)
        self.assertEqual(1, len(items))
        self.assertEqual("claude_code_http_mimicry_manifest", items[0]["field"])

    def test_manifest_beta_set_drift(self) -> None:
        live = {
            "claude_code_user_agent_version": "2.1.159",
            "claude_code_http_mimicry_manifest": self._manifest(haiku=["oauth-2025-04-20", "extra-beta"]),
        }
        items = mgr._http_ua_drift_items("edge:us1", live, self.EXPECTED)
        self.assertEqual(1, len(items))
        self.assertIn("haiku", items[0]["warning"])

    def test_manifest_invalid_json_is_drift(self) -> None:
        live = {
            "claude_code_user_agent_version": "2.1.159",
            "claude_code_http_mimicry_manifest": "{not-json",
        }
        items = mgr._http_ua_drift_items("edge:us1", live, self.EXPECTED)
        self.assertEqual(1, len(items))
        self.assertEqual("drift", items[0]["status"])


if __name__ == "__main__":
    unittest.main()
