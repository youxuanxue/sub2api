#!/usr/bin/env python3
"""Behavior and safety contracts for capacity-first planning."""

from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

_DIR = pathlib.Path(__file__).resolve().parent
_TEST = pathlib.Path(__file__).resolve()
_PROBE = _DIR / "probe-data-layer-capacity-prototype.sh"
_VERDICT = _DIR / "data_layer_capacity_verdict_prototype.py"
_PROJECTION = _DIR / "data_layer_capacity_projection.py"


def _load_module(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


verdict = _load_module("data_layer_capacity_verdict_prototype", _VERDICT)
projection = _load_module("data_layer_capacity_projection", _PROJECTION)


class DataLayerCapacitySafetyTest(unittest.TestCase):
    def test_prototype_is_not_wired_to_prod_workflows(self) -> None:
        repo_root = _DIR.parents[1]
        consumers = [repo_root / ".github", repo_root / "ops"]
        references: list[str] = []
        for root in consumers:
            for path in root.rglob("*"):
                if not path.is_file() or path.resolve() in {_TEST, _PROBE, _VERDICT}:
                    continue
                try:
                    body = path.read_text(encoding="utf-8")
                except UnicodeDecodeError:
                    continue
                if _PROBE.name in body or _VERDICT.name in body:
                    references.append(str(path.relative_to(repo_root)))
        self.assertEqual(references, [], msg=f"prototype unexpectedly activated by {references}")

    def test_growth_timeout_is_unknown_not_green(self) -> None:
        thresholds = {
            "months_to_volume_full": {"approaching": 6, "trigger": 3},
            "usage_logs_pct_of_volume": {"approaching": 25, "trigger": 40},
        }
        out = verdict.compute_verdict(
            {
                "usage_logs_bytes": 5 * 1024**3,
                "usage_logs_rows": 8_000_000,
                "catalog_probe_ok": True,
                "growth_probe_ok": False,
                "df_total_bytes": 50 * 1024**3,
                "df_avail_bytes": 13 * 1024**3,
            },
            thresholds,
        )
        self.assertEqual(out["verdict"], "unknown")
        self.assertIsNone(out["monthly_growth_gib"])

    def test_missing_catalog_snapshot_is_unknown_not_green(self) -> None:
        thresholds = {
            "months_to_volume_full": {"approaching": 6, "trigger": 3},
            "usage_logs_pct_of_volume": {"approaching": 25, "trigger": 40},
        }
        out = verdict.compute_verdict(
            {
                "growth_probe_ok": True,
                "usage_logs_rows_30d": 0,
                "df_total_bytes": 50 * 1024**3,
                "df_avail_bytes": 30 * 1024**3,
            },
            thresholds,
        )
        self.assertEqual(out["verdict"], "unknown")
        self.assertIn("catalog capacity probe", out["summary"])

        partial = verdict.compute_verdict(
            {
                "catalog_probe_ok": True,
                "growth_probe_ok": True,
                "usage_logs_rows_30d": 0,
                "df_total_bytes": 50 * 1024**3,
                "df_avail_bytes": 30 * 1024**3,
            },
            thresholds,
        )
        self.assertEqual(partial["verdict"], "unknown")
        self.assertIn("usage_logs_bytes", partial["summary"])

    def test_tagged_probe_parser_merges_bounded_growth(self) -> None:
        stats = verdict._parse_probe_stdin(
            "\n".join(
                [
                    'PGSTATS {"catalog_probe_ok":true,"usage_logs_bytes":100,"usage_logs_rows":10}',
                    'PGGROWTH {"growth_probe_ok":true,"usage_logs_rows_30d":4}',
                    'DFSTATS {"df_total_bytes":1000,"df_avail_bytes":500}',
                ]
            )
        )
        self.assertEqual(stats["usage_logs_rows_30d"], 4)
        self.assertTrue(stats["growth_probe_ok"])

    def test_projection_requires_explicit_reclaim_and_residual_growth(self) -> None:
        snapshot = {
            "usage_logs_bytes": 5.4 * 1024**3,
            "usage_logs_rows": 9_000_000,
            "usage_logs_rows_30d": 6_000_000,
            "ops_system_logs_bytes": 7 * 1024**3,
            "ops_error_logs_bytes": 5 * 1024**3,
            "catalog_probe_ok": True,
            "growth_probe_ok": True,
            "df_total_bytes": 50 * 1024**3,
            "df_used_bytes": 36.8 * 1024**3,
        }
        proc = subprocess.run(
            ["python3", str(_PROJECTION)],
            input=json.dumps(snapshot),
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 2)
        self.assertIn("planning assumptions must be explicit", proc.stderr)

        with self.assertRaisesRegex(ValueError, "snapshot missing required fields"):
            projection.project_capacity(
                {
                    "catalog_probe_ok": True,
                    "growth_probe_ok": True,
                    "df_total_bytes": 50 * 1024**3,
                    "df_used_bytes": 36.8 * 1024**3,
                    "usage_logs_rows": 9_000_000,
                    "usage_logs_rows_30d": 6_000_000,
                    "ops_system_logs_bytes": 7 * 1024**3,
                    "ops_error_logs_bytes": 5 * 1024**3,
                },
                target_volume_gib=100,
                usage_hot_days=90,
                ops_reclaim_gib_low=5,
                ops_reclaim_gib_high=10,
                residual_growth_gib_per_month=0.5,
                operational_limit_pct=85,
            )

    def test_projection_models_100_gib_without_live_calls(self) -> None:
        out = projection.project_capacity(
            {
                "usage_logs_bytes": 5.4 * 1024**3,
                "usage_logs_rows": 9_000_000,
                "usage_logs_rows_30d": 6_000_000,
                "ops_system_logs_bytes": 7 * 1024**3,
                "ops_error_logs_bytes": 5 * 1024**3,
                "catalog_probe_ok": True,
                "growth_probe_ok": True,
                "df_total_bytes": 50 * 1024**3,
                "df_used_bytes": 36.8 * 1024**3,
            },
            target_volume_gib=100,
            usage_hot_days=90,
            ops_reclaim_gib_low=5,
            ops_reclaim_gib_high=10,
            residual_growth_gib_per_month=0.5,
            operational_limit_pct=85,
        )
        self.assertEqual(out["mode"], "offline_projection")
        self.assertEqual(out["derived"]["usage_steady_gib"], 10.8)
        self.assertEqual(
            [scenario["projected_used_gib"] for scenario in out["scenarios"]],
            [37.2, 32.2],
        )
        self.assertIn("DELETE alone", out["warning"])
        self.assertEqual(out["current"]["observed_ops_relation_gib"], 12.0)

        with self.assertRaisesRegex(ValueError, "cannot exceed observed ops relation size"):
            projection.project_capacity(
                {
                    "usage_logs_bytes": 5.4 * 1024**3,
                    "usage_logs_rows": 9_000_000,
                    "usage_logs_rows_30d": 6_000_000,
                    "ops_system_logs_bytes": 7 * 1024**3,
                    "ops_error_logs_bytes": 5 * 1024**3,
                    "catalog_probe_ok": True,
                    "growth_probe_ok": True,
                    "df_total_bytes": 50 * 1024**3,
                    "df_used_bytes": 36.8 * 1024**3,
                },
                target_volume_gib=100,
                usage_hot_days=90,
                ops_reclaim_gib_low=5,
                ops_reclaim_gib_high=13,
                residual_growth_gib_per_month=0.5,
                operational_limit_pct=85,
            )

    def test_probe_is_read_only_and_scan_bounded(self) -> None:
        body = _PROBE.read_text(encoding="utf-8")
        self.assertIn("pg_stat_user_tables", body)
        self.assertIn("pg_partition_tree", body)
        self.assertIn("qa_records_partitioned", body)
        self.assertIn("WHERE isleaf", body)
        self.assertNotIn("(SELECT count(*) FROM usage_logs)", body)

        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            bin_path = tmp_path / "bin"
            bin_path.mkdir()
            capture_path = tmp_path / "docker-args.txt"
            docker_stub = bin_path / "docker"
            docker_stub.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    printf '%s\n' "$*" >>"$CAPTURE_PATH"
                    if [[ "$*" == *"PGSTATS "* || "$*" == *"PGGROWTH "* ]]; then
                      exit 1
                    fi
                    """
                ),
                encoding="utf-8",
            )
            docker_stub.chmod(0o755)
            df_stub = bin_path / "df"
            df_stub.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    printf 'Filesystem 1-blocks Used Available Capacity Mounted on\n'
                    printf '/dev/mock 53687091200 21474836480 32212254720 40%% /var/lib/tokenkey\n'
                    """
                ),
                encoding="utf-8",
            )
            df_stub.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{bin_path}:{env['PATH']}"
            env["CAPTURE_PATH"] = str(capture_path)
            proc = subprocess.run(
                ["bash", str(_PROBE)],
                capture_output=True,
                text=True,
                check=False,
                env=env,
            )
            docker_args = capture_path.read_text(encoding="utf-8")

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIn('PGSTATS {"catalog_probe_ok":false', proc.stdout)
        self.assertIn('PGGROWTH {"growth_probe_ok":false', proc.stdout)
        self.assertIn("DFSTATS", proc.stdout)
        self.assertIn("PGOPTIONS=-c default_transaction_read_only=on", docker_args)
        self.assertIn("-c lock_timeout=100ms", docker_args)
        self.assertIn("-c statement_timeout=2s", docker_args)

    def test_probe_shell_parses(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_PROBE)],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)


if __name__ == "__main__":
    unittest.main()
