"""The node1 mirror timer: what must hold for the third copy to be real."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

HERE = pathlib.Path(__file__).resolve().parent


def _load(name: str):
    spec = importlib.util.spec_from_file_location(name, HERE / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    assert spec and spec.loader
    spec.loader.exec_module(module)
    return module


installer = _load("install_node1_db_mirror")


class ScheduleTest(unittest.TestCase):
    def test_cadence_keeps_up_with_cos_retention(self) -> None:
        # COS keeps 7 days of fulls and an incremental lands every two hours,
        # so four hours never falls more than one increment behind.
        self.assertEqual(installer.SCHEDULE, "*-*-* 00/4:17:00 UTC")

    def test_timer_survives_a_reboot_and_spreads_load(self) -> None:
        timer = installer.timer_unit()
        self.assertIn("Persistent=true", timer)
        self.assertIn("OnBootSec=", timer)
        self.assertIn("RandomizedDelaySec=", timer)


class ServiceUnitTest(unittest.TestCase):
    def test_fetch_yields_to_everything_else_on_node1(self) -> None:
        unit = installer.service_unit(["prod"])
        self.assertIn("Nice=19", unit)
        self.assertIn("IOSchedulingClass=idle", unit)
        self.assertIn("UMask=0077", unit)

    def test_credentials_come_from_a_file_not_the_unit(self) -> None:
        unit = installer.service_unit(["prod"])
        self.assertIn(f"EnvironmentFile={installer.ENV_FILE}", unit)
        for leak in ("tencent_SecretId=", "tencent_SecretKey=", "AKID"):
            self.assertNotIn(leak, unit)

    def test_live_prod_coordinates_reach_the_exec_line(self) -> None:
        unit = installer.service_unit(["prod"])
        exec_line = next(l for l in unit.splitlines() if l.startswith("ExecStart="))
        self.assertIn("--bucket tokenkey-prod-backup-usw-1320330536", exec_line)
        self.assertIn("--prefix prod/postgres", exec_line)
        self.assertIn("--endpoint cos.na-siliconvalley.myqcloud.com", exec_line)
        self.assertIn("--region na-siliconvalley", exec_line)
        self.assertIn(f"--destination {installer.ARCHIVE_ROOT}", exec_line)

    def test_timeout_covers_a_first_full_sync(self) -> None:
        # 14.29 GiB at the measured 29.7 MiB/s is about 8 minutes; the timeout
        # has to tolerate a much slower link without killing a live fetch.
        unit = installer.service_unit(["prod"])
        timeout = next(l for l in unit.splitlines() if l.startswith("TimeoutStartSec="))
        self.assertGreaterEqual(int(timeout.split("=")[1]), 3600)


class EnvironmentGuardTest(unittest.TestCase):
    def test_both_environments_are_fully_specified(self) -> None:
        for name in ("prod", "cn-prod"):
            spec = installer.ENVIRONMENTS[name]
            for key in ("bucket", "prefix", "endpoint", "region"):
                with self.subTest(env=name, key=key):
                    self.assertTrue(spec[key], f"{name} is missing {key}")

    def test_the_two_buckets_are_distinct_and_regional(self) -> None:
        # Pointing both environments at one bucket would mirror prod twice and
        # leave cn-prod with no third copy at all.
        prod = installer.ENVIRONMENTS["prod"]
        cn = installer.ENVIRONMENTS["cn-prod"]
        self.assertNotEqual(prod["bucket"], cn["bucket"])
        self.assertNotEqual(prod["prefix"], cn["prefix"])
        self.assertIn("siliconvalley", prod["endpoint"])
        self.assertIn("ap-beijing", cn["endpoint"])

    def test_an_unknown_environment_is_refused(self) -> None:
        self.assertNotIn("staging", installer.ENVIRONMENTS)

    def test_a_missing_coordinate_blocks_install(self) -> None:
        # The guard must stay even though both are populated today: a future
        # environment added with blanks would otherwise fetch from "".
        source = (HERE / "install_node1_db_mirror.py").read_text()
        self.assertIn("has no COS coordinates yet", source)
        self.assertIn('for k in ("bucket", "endpoint", "region")', source)


class RunnerScriptTest(unittest.TestCase):
    def test_runner_delegates_to_the_verified_mirror(self) -> None:
        # The runner must not re-implement copying: the manifest, per-file
        # sha256, staging rename, capacity policy and receipt all live in
        # cos_node1_mirror and are tested there.
        script = installer.runner_script()
        self.assertIn("cos_node1_mirror.py", script)
        self.assertIn("--namespace", script)
        self.assertIn("--apply", script)
        for reimplementation in ("shutil.copy", "boto3", "get_object", "sha256"):
            self.assertNotIn(reimplementation, script)

    def test_snapshot_name_is_stable_within_a_day(self) -> None:
        # cos_node1_mirror short-circuits on an existing verified receipt, so a
        # re-run only copies nothing if the name it derives is the same. Execute
        # the derivation rather than grepping the source for a comment.
        import datetime as dt
        import re
        script = installer.runner_script()
        expr = re.search(r'snapshot = (f"[^"]+")', script)
        self.assertIsNotNone(expr, "the runner must derive a snapshot name")
        args = type("A", (), {"environment": "prod"})()
        first = eval(expr.group(1), {"args": args, "dt": dt})
        second = eval(expr.group(1), {"args": args, "dt": dt})
        self.assertEqual(first, second)
        self.assertRegex(first, r"^prod-\d{8}$")

    def test_runner_is_valid_python(self) -> None:
        compile(installer.runner_script(), "mirror_once.py", "exec")


if __name__ == "__main__":
    unittest.main()
