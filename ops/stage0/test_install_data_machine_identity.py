#!/usr/bin/env python3
"""install-data-machine-identity.sh writes nothing without the confirm and its preconditions."""
from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "ops" / "stage0" / "install-data-machine-identity.sh"
BUNDLE = {
    "prod": [ROOT / "ops/stage0/tencent_backup_identity.py", ROOT / "ops/stage0/postgres_backup.py",
             ROOT / "deploy/tencent/candidate-usw-20261022.json"],
    "cn-prod": [ROOT / "deploy/domestic/cn_backup_identity.py", ROOT / "deploy/domestic/prod-target.json"],
}
DATA_HOST = {
    "prod": json.loads((ROOT / "deploy/tencent/candidate-usw-20261022.json").read_text())["host_watch"]["data_hostname"],
    "cn-prod": json.loads((ROOT / "deploy/domestic/prod-target.json").read_text())["data_instance_name"],
}
UNIT = {"prod": "tokenkey-backup-identity", "cn-prod": "tokenkey-cn-backup-identity"}
# Calls that change the host. Plan mode and every refusal must issue none.
WRITES = ("systemctl start", "systemctl enable", "systemctl daemon-reload")


class InstallDataMachineIdentityTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = pathlib.Path(tmp.name)
        self.bin = self.dir / "bin"
        self.bin.mkdir()
        self.calls = self.dir / "calls"
        self.units = self.dir / "units"
        self.units.mkdir()
        self.lib = self.dir / "lib"
        self.config = self.dir / "pgbackrest-cos" / "pgbackrest.conf"
        self.config.parent.mkdir()
        self.config.write_text("previous")
        self.credentials = self.dir / "cos-backup.env"

    def stub(self, site: str, *, hostname: str | None = None, running: bool = True,
             credential: str | None = "root 600", refresh_ok: bool = True) -> None:
        log = self.calls
        config_stat = "root 70 640"
        scripts = {
            "hostname": f"echo {hostname or DATA_HOST[site]}",
            # Only the database server answers Running=true; the resolver
            # inspects every candidate name and needs exactly one.
            "docker": (f'echo "docker $*" >> "{log}"\n'
                       '[ "$1" = inspect ] || exit 0\n'
                       f'[ "${{@: -1}}" = tokenkey-postgres ] && {{ echo {"true" if running else "false"}; exit 0; }}\n'
                       'exit 1'),
            "systemctl": (f'echo "systemctl $*" >> "{log}"\n'
                          f'[ "$1 $2" = "start {UNIT[site]}.service" ] && exit {0 if refresh_ok else 1}\n'
                          '[ "$1" = is-active ] && echo active\nexit 0'),
            "systemd-analyze": f'echo "systemd-analyze $*" >> "{log}"; exit 0',
            # Owner/mode answers the tests control; GNU stat -c is not on macOS.
            "stat": (f'case "${{@: -1}}" in "{self.credentials}") echo "{credential or ""}" ;; '
                     f'"{self.config}") echo "{config_stat}" ;; *) exit 1 ;; esac'),
        }
        for name, body in scripts.items():
            path = self.bin / name
            path.write_text("#!/bin/bash\n" + body + "\n")
            path.chmod(0o755)
        if credential is not None:
            self.credentials.write_text("tencent_SecretId=a\ntencent_SecretKey=b\n")

    def run_script(self, site: str, **env: str) -> tuple[subprocess.CompletedProcess, str]:
        bundle = self.dir / "bundle"
        bundle.mkdir(exist_ok=True)
        for source in (*BUNDLE[site], ROOT / "ops/lib/resolve-app-container.sh"):
            shutil.copy(source, bundle / source.name)
        proc = subprocess.run(
            ["bash", str(SCRIPT)],
            env={**os.environ, "PATH": f"{self.bin}:{os.environ['PATH']}", "SITE": site,
                 "BUNDLE_DIR": str(bundle), "UNIT_DIR": str(self.units), "LIB_ROOT": str(self.lib),
                 "CREDENTIALS": str(self.credentials), "DATA_CONFIG": str(self.config), **env},
            capture_output=True, text=True, check=False,
        )
        return proc, self.calls.read_text() if self.calls.exists() else ""

    def assert_no_writes(self, calls: str) -> None:
        for write in WRITES:
            self.assertNotIn(write, calls)
        self.assertEqual(list(self.units.iterdir()), [])
        self.assertFalse(self.lib.exists())

    def test_plan_lists_the_change_and_writes_nothing(self) -> None:
        for site in ("prod", "cn-prod"):
            with self.subTest(site=site):
                self.setUp()
                self.stub(site)
                proc, calls = self.run_script(site)
                self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
                self.assertIn(f"{UNIT[site]}.timer create", proc.stdout)
                self.assertIn(f"TK_DATA_IDENTITY_CONFIRM=install-data-identity-{site}-{DATA_HOST[site]}", proc.stdout)
                self.assert_no_writes(calls)

    def test_apply_installs_the_refresher_and_checks_the_archive(self) -> None:
        site = "prod"
        self.stub(site)
        proc, calls = self.run_script(site, MODE="apply",
                                      TK_DATA_IDENTITY_CONFIRM=f"install-data-identity-{site}-{DATA_HOST[site]}")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("RESULT installed", proc.stdout)
        service = (self.units / f"{UNIT[site]}.service").read_text()
        self.assertIn("tencent_backup_identity.py refresh", service)
        self.assertIn("TK_TENCENT_PROD_MATRIX=", service)
        self.assertIn("OnUnitActiveSec=15min", (self.units / f"{UNIT[site]}.timer").read_text())
        self.assertTrue((self.lib / "backup-identity" / "postgres_backup.py").is_file())
        # Refresh before enabling the timer, then prove the new config with archive-check.
        order = [line for line in calls.splitlines() if line.startswith(("systemctl start", "systemctl enable"))]
        self.assertEqual(order, [f"systemctl start {UNIT[site]}.service",
                                 f"systemctl enable --now {UNIT[site]}.timer",
                                 "systemctl start tokenkey-postgres-archive-check.service"])

    def test_failed_first_refresh_does_not_enable_the_timer(self) -> None:
        site = "cn-prod"
        self.stub(site, refresh_ok=False)
        proc, calls = self.run_script(site, MODE="apply",
                                      TK_DATA_IDENTITY_CONFIRM=f"install-data-identity-{site}-{DATA_HOST[site]}")
        self.assertEqual(proc.returncode, 3, proc.stdout + proc.stderr)
        self.assertIn("timer not enabled", proc.stderr)
        self.assertNotIn("systemctl enable", calls)

    def test_apply_refuses_without_the_exact_confirm(self) -> None:
        for confirm in ("", "install-data-identity-cn-prod-VM-0-3-ubuntu", "install-data-identity-prod-other"):
            with self.subTest(confirm=confirm):
                self.setUp()
                self.stub("prod")
                proc, calls = self.run_script("prod", MODE="apply", TK_DATA_IDENTITY_CONFIRM=confirm)
                self.assertEqual(proc.returncode, 64, proc.stdout + proc.stderr)
                self.assert_no_writes(calls)

    def test_refuses_on_the_app_host(self) -> None:
        for site, app_host in (("prod", "VM-0-16-ubuntu"), ("cn-prod", "tokenkey-cn-prod")):
            with self.subTest(site=site):
                self.setUp()
                self.stub(site, hostname=app_host)
                proc, calls = self.run_script(site, MODE="apply",
                                              TK_DATA_IDENTITY_CONFIRM=f"install-data-identity-{site}-{app_host}")
                self.assertEqual(proc.returncode, 2, proc.stdout + proc.stderr)
                self.assertIn(f"the {site} data machine is {DATA_HOST[site]}", proc.stderr)
                self.assert_no_writes(calls)

    def test_refuses_without_a_safe_credential_file(self) -> None:
        for credential, message in ((None, "cos-backup.env missing"), ("root 644", "must be root-owned 0600")):
            with self.subTest(credential=credential):
                self.setUp()
                self.stub("prod", credential=credential)
                proc, calls = self.run_script("prod", MODE="apply",
                                              TK_DATA_IDENTITY_CONFIRM=f"install-data-identity-prod-{DATA_HOST['prod']}")
                self.assertEqual(proc.returncode, 2, proc.stdout + proc.stderr)
                self.assertIn(message, proc.stderr)
                self.assert_no_writes(calls)

    def test_refuses_when_the_database_is_not_here(self) -> None:
        self.stub("cn-prod", running=False)
        proc, calls = self.run_script("cn-prod")
        self.assertEqual(proc.returncode, 2)
        self.assertIn("no running Postgres server here", proc.stderr)
        self.assert_no_writes(calls)


if __name__ == "__main__":
    unittest.main()
