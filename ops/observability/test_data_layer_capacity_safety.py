#!/usr/bin/env python3
"""Behavior and safety contracts for capacity-first planning."""

from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import re
import subprocess
import tempfile
import textwrap
import unittest

_DIR = pathlib.Path(__file__).resolve().parent
_PROBE = _DIR / "probe-data-layer-capacity.sh"
_VERDICT = _DIR / "data_layer_capacity_verdict.py"
_PROJECTION = _DIR / "data_layer_capacity_projection.py"


def _load_module(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


verdict = _load_module("data_layer_capacity_verdict", _VERDICT)
projection = _load_module("data_layer_capacity_projection", _PROJECTION)


_THRESHOLDS = {
    "months_to_volume_full": {"approaching": 6, "trigger": 3},
    "usage_logs_pct_of_volume": {"approaching": 25, "trigger": 40},
}


def _window(window_bytes, window_days=30, window_rows_est=0, span=30):
    """Build the PGWINDOW shape the byte-based growth path consumes."""
    return {
        "window_probe_ok": True,
        "window_span_days": span,
        "window_tables": {
            "usage_logs": {
                "window_bytes": window_bytes,
                "window_days": window_days,
                "window_rows_est": window_rows_est,
                "leaf_parts": 82,
                "unbounded_parts": 1,
                "total_bytes": window_bytes,
            }
        },
    }


class DataLayerCapacitySafetyTest(unittest.TestCase):
    def test_every_probe_tag_reaches_the_verdict(self) -> None:
        """Each tagged line the probe emits must be parsed, not silently dropped.

        DFBACKING was added to the probe and to compute_verdict but not to the
        parser's tag list, so the field arrived as None and the verdict kept using
        the optimistic inner df -- reporting 11.9 months of runway on live prod
        where the binding constraint gave 6.2. It failed as a null, not an error.
        """
        probe = pathlib.Path(__file__).with_name("probe-data-layer-capacity.sh").read_text(
            encoding="utf-8"
        )
        emitted = set(re.findall(r"^\s*(?:echo|printf)[^\n]*?\b([A-Z]{2,}[A-Z_]*)\s*\{", probe, re.M))
        emitted |= set(re.findall(r"printf \"([A-Z]{2,}[A-Z_]*) \{", probe))
        emitted = {tag for tag in emitted if tag not in {"ERROR", "WARN"}}
        self.assertTrue(emitted, "probe emits no recognizable tagged lines")
        source = pathlib.Path(__file__).with_name("data_layer_capacity_verdict.py").read_text(
            encoding="utf-8"
        )
        parsed = re.search(r"for tag in \(([^)]*)\)", source)
        self.assertIsNotNone(parsed, "parser tag list not found")
        known = set(re.findall(r'"([A-Z_]+)"', parsed.group(1)))
        missing = sorted(emitted - known)
        self.assertEqual(
            missing, [], f"probe emits {missing} but the verdict parser ignores them"
        )

    def test_backing_filesystem_binds_the_runway(self) -> None:
        """A loop/LUKS data volume must not be judged by the inner df alone."""
        snapshot = verdict._parse_probe_stdin(
            'DFSTATS {"df_total_bytes":400000000000,"df_avail_bytes":200000000000}\n'
            'DFBACKING {"backing_avail_bytes":8000000000,"backing_mount":"/"}\n'
        )
        self.assertEqual(snapshot.get("backing_avail_bytes"), 8000000000)
        self.assertEqual(snapshot.get("backing_mount"), "/")

    def test_losing_every_growth_signal_is_unknown_not_green(self) -> None:
        """With no growth signal of either kind, abstain.

        Both sources are gone here: the row scan failed AND no PGWINDOW line was
        emitted. A failed row scan alone no longer blocks the verdict -- the byte
        path owns growth and is covered by
        test_restored_host_shape_still_produces_a_verdict, which is the live-prod
        case where that scan times out.
        """
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

    def test_growth_is_measured_in_bytes_not_derived_from_cardinality(self) -> None:
        """Growth must not consult any row count.

        The old formula was rows_30d * (bytes / rows) with `rows` from
        pg_stat_user_tables.n_live_tup -- a cumulative counter kept in the stats
        file, not the data files. pgBackRest restores data files, so the counter
        starts at zero and the ratio becomes fiction. Here the stats say the table
        holds 10 rows while the window holds 5GiB: if the verdict still touched
        cardinality, the implied growth would be astronomically wrong.
        """
        out = verdict.compute_verdict(
            {
                "usage_logs_bytes": 10 * 1024**3,
                "usage_logs_rows": 10,            # absurd on purpose
                "usage_logs_rows_30d": 5_000_000,
                "catalog_probe_ok": True,
                "growth_probe_ok": True,
                "df_total_bytes": 100 * 1024**3,
                "df_avail_bytes": 60 * 1024**3,
                **_window(5 * 1024**3),
            },
            _THRESHOLDS,
        )
        self.assertEqual(out["monthly_growth_source"], "partition_bytes")
        self.assertAlmostEqual(out["monthly_growth_gib"], 5.0, places=2)
        self.assertEqual(out["months_to_volume_full"], 12.0)
        self.assertEqual(out["verdict"], "green")

    def test_restored_host_shape_still_produces_a_verdict(self) -> None:
        """A pgBackRest-restored host must not degrade the gate.

        Live-prod shape: n_live_tup near zero, 1 of 82 leaf partitions ever
        analyzed, and the 30-day row scan timing out at statement_timeout=2s.
        81 partitions carry n_mod_since_analyze=0 -- a day partition stops being
        written once it rolls over, so autoanalyze never fires again and ANALYZE
        would have to be re-run by hand after every restore, host move and standby
        promotion. Byte accounting removes the dependency instead.
        """
        out = verdict.compute_verdict(
            {
                "usage_logs_bytes": 7657177088,
                "usage_logs_rows": 24685,
                "usage_logs_reltuples": 7717412,
                "usage_logs_leaf_partitions": 82,
                "usage_logs_analyzed_partitions": 1,
                "catalog_probe_ok": True,
                "growth_probe_ok": False,       # the 5M-row scan timed out
                "usage_logs_rows_30d": None,
                "df_total_bytes": 83938926592,
                "df_avail_bytes": 57783738368,
                **_window(5130936320, window_rows_est=5030000),
            },
            _THRESHOLDS,
        )
        self.assertEqual(out["verdict"], "green")
        self.assertAlmostEqual(out["monthly_growth_gib"], 4.78, places=1)
        self.assertGreater(out["months_to_volume_full"], 6)
        # The independent reltuples estimate corroborates the byte path. It is
        # reported, never acted on: reltuples is a catalog field and survives a
        # restore, but the byte path is the one that cannot be invalidated at all.
        self.assertIn("cross_check", out)
        self.assertLess(abs(out["cross_check"]["ratio_to_primary"] - 1.0), 0.1)

    def test_cross_check_absence_never_blocks_the_verdict(self) -> None:
        out = verdict.compute_verdict(
            {
                "usage_logs_bytes": 7657177088,
                "usage_logs_reltuples": 0,       # nothing analyzed at all
                "catalog_probe_ok": True,
                "growth_probe_ok": False,
                "df_total_bytes": 83938926592,
                "df_avail_bytes": 57783738368,
                **_window(5130936320),
            },
            _THRESHOLDS,
        )
        self.assertEqual(out["verdict"], "green")
        self.assertNotIn("cross_check", out)

    def test_missing_byte_accounting_never_falls_back_to_cardinality(self) -> None:
        """Abstaining is the only acceptable answer when byte accounting is gone.

        The stats fields are present and tempting in this snapshot; using them is
        exactly the behavior that produced the false trigger.
        """
        out = verdict.compute_verdict(
            {
                "usage_logs_bytes": 7657177088,
                "usage_logs_rows": 24685,
                "usage_logs_reltuples": 7717412,
                "usage_logs_rows_30d": 5_000_000,
                "catalog_probe_ok": True,
                "growth_probe_ok": True,
                "df_total_bytes": 83938926592,
                "df_avail_bytes": 57783738368,
                "window_probe_ok": False,
                "window_span_days": None,
                "window_tables": None,
            },
            _THRESHOLDS,
        )
        self.assertEqual(out["verdict"], "unknown")
        self.assertIsNone(out["monthly_growth_gib"])
        # Occupancy and free space were measured, so they are still reported: a
        # nearly full volume must stay visible even when growth is unusable.
        self.assertEqual(out["usage_logs_pct_of_volume"], 9.1)
        self.assertIsNotNone(out["df_avail_gib"])

    def test_retention_shorter_than_window_is_scaled(self) -> None:
        """Unretained days are not zero growth."""
        scaled = verdict.compute_verdict(
            {
                "usage_logs_bytes": 5 * 1024**3,
                "catalog_probe_ok": True,
                "growth_probe_ok": True,
                "df_total_bytes": 30 * 1024**3,
                "df_avail_bytes": 10 * 1024**3,
                **_window(1 * 1024**3, window_days=7),
            },
            _THRESHOLDS,
        )
        self.assertAlmostEqual(scaled["monthly_growth_gib"], 30 / 7, places=2)
        self.assertIn("scaled from 7/30d", scaled["summary"])
        self.assertEqual(scaled["verdict"], "trigger")

    def test_empty_window_is_unknown_not_infinite_runway(self) -> None:
        """No day partitions in the window is a broken maintainer, not safety."""
        out = verdict.compute_verdict(
            {
                "usage_logs_bytes": 5 * 1024**3,
                "catalog_probe_ok": True,
                "growth_probe_ok": True,
                "df_total_bytes": 30 * 1024**3,
                "df_avail_bytes": 10 * 1024**3,
                **_window(0, window_days=0),
            },
            _THRESHOLDS,
        )
        self.assertEqual(out["verdict"], "unknown")
        self.assertIsNone(out["months_to_volume_full"])

    def test_probe_window_sql_is_catalog_only_and_utc_bucketed(self) -> None:
        body = _PROBE.read_text(encoding="utf-8")
        self.assertIn("PGWINDOW", body)
        self.assertIn("relpartbound", body)
        self.assertIn("pg_total_relation_size", body)
        # UTC bucketing: the container runs TimeZone=Asia/Tokyo and bounds are
        # stored as '... 09:00:00+09', so a local-time window shifts by a day.
        self.assertIn("now() AT TIME ZONE 'UTC'", body)
        # Catalog-only: the window must never scan the ledger itself.
        window_sql = body[body.index("PGWINDOW (partition"):body.index("PGGROWTH (bounded")]
        self.assertNotIn("FROM usage_logs", window_sql)
        self.assertNotIn("n_live_tup", window_sql)
        # MINVALUE partitions have no parsable lower bound; they must be counted
        # and reported rather than silently dropped.
        self.assertIn("unbounded_parts", window_sql)

    def test_tagged_probe_parser_merges_window_bytes(self) -> None:
        stats = verdict._parse_probe_stdin(
            "\n".join(
                [
                    'PGSTATS {"catalog_probe_ok":true,"usage_logs_bytes":100}',
                    'PGWINDOW {"window_probe_ok":true,"window_span_days":30,'
                    '"window_tables":{"usage_logs":{"window_bytes":40,"window_days":30}}}',
                    'DFSTATS {"df_total_bytes":1000,"df_avail_bytes":500}',
                ]
            )
        )
        # Namespaced key: a generic "tables" would collide in the flat merge.
        self.assertEqual(stats["window_tables"]["usage_logs"]["window_bytes"], 40)
        self.assertTrue(stats["window_probe_ok"])

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

    def test_probe_pgstats_serializes_catalog_success_flag(self) -> None:
        body = _PROBE.read_text(encoding="utf-8")
        serialized_projection = body.split("SELECT 'PGSTATS '", 1)[1].split("FROM flags", 1)[0]
        self.assertIn("catalog_probe_ok", serialized_projection)

    def test_probe_is_read_only_and_scan_bounded(self) -> None:
        body = _PROBE.read_text(encoding="utf-8")
        self.assertIn("pg_stat_user_tables", body)
        self.assertIn("pg_partition_tree", body)
        self.assertIn("usage_logs_partitioned", body)
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
                    # The probe resolves its database container through
                    # ops/lib/resolve-app-container.sh before querying, so the
                    # stub answers the Stage0 name as a running container.
                    if [[ "$1" == inspect && "$*" == *State.Running* ]]; then
                      [[ "${@: -1}" == tokenkey-postgres ]] && printf 'true\n' || exit 1
                      exit 0
                    fi
                    if [[ "$1" == ps ]]; then
                      printf 'tokenkey-postgres\n'
                      exit 0
                    fi
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
