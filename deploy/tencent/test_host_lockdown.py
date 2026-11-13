import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCK = ROOT / "deploy/tencent/lock-down-sshd.sh"
INSTALL = ROOT / "deploy/tencent/install-cn-socks-proxy.sh"
SOURCES = ("43.162.83.233", "43.162.126.44", "154.8.214.179", "106.75.29.139")


class HostLockdownTest(unittest.TestCase):
    def test_sshd_dropin_beats_later_password_login(self):
        with tempfile.TemporaryDirectory() as tmp:
            dropin = Path(tmp) / "00-tokenkey-lockdown.conf"
            env = os.environ.copy()
            env.update(TOKENKEY_SSH_LOCK_DRY="1", TOKENKEY_SSHD_DROPIN=str(dropin))
            subprocess.check_call(["bash", str(LOCK)], env=env)
            text = dropin.read_text()
            for line in (
                "PasswordAuthentication no",
                "PermitRootLogin no",
                "AllowTcpForwarding no",
            ):
                self.assertIn(line, text)
            sshd = shutil.which("sshd") or next(
                (candidate for candidate in ("/usr/sbin/sshd", "/usr/libexec/sshd")
                 if Path(candidate).is_file()),
                None,
            )
            if sshd is None:
                self.skipTest("sshd is not installed")
            hostkey = Path(tmp) / "hostkey"
            subprocess.check_call(
                ["ssh-keygen", "-q", "-t", "ed25519", "-f", str(hostkey), "-N", ""],
            )
            parent = Path(tmp) / "sshd_config"
            parent.write_text(
                f"HostKey {hostkey}\n"
                f"Include {dropin}\n"
                "PasswordAuthentication yes\n"
                "PermitRootLogin yes\n"
                "AllowTcpForwarding yes\n"
            )
            completed = subprocess.run(
                [sshd, "-T", "-f", str(parent)],
                text=True, capture_output=True, check=False,
            )
            if completed.returncode != 0:
                self.skipTest(completed.stderr.strip() or "sshd -T unavailable")
            values = {}
            for line in completed.stdout.splitlines():
                key, _, value = line.partition(" ")
                values.setdefault(key, value)
            self.assertEqual(values["passwordauthentication"], "no")
            self.assertEqual(values["permitrootlogin"], "no")
            self.assertEqual(values["allowtcpforwarding"], "no")

    def test_proxy_installer_keeps_1080_sources_and_drops_ssh_and_8080(self):
        env = os.environ.copy()
        env["TOKENKEY_PRINT_FW_RULES"] = "1"
        rules = subprocess.check_output(["bash", str(INSTALL)], env=env, text=True)
        for source in SOURCES:
            self.assertIn(f"-s {source}/32 -j ACCEPT;", rules)
        self.assertIn("--dport 1080 -j TK_CN_PROXY", rules)
        for port in ("22", "8080"):
            self.assertIn(f"--dport {port} -m conntrack --ctstate NEW -j DROP", rules)
            self.assertNotIn(f"--dport {port} -j DROP", rules)
        self.assertNotIn("0.0.0.0/0", rules)
        for script in (
            ROOT / "deploy/tencent/bootstrap-prod-app.sh",
            ROOT / "deploy/tencent/install-cn-socks-proxy.sh",
            ROOT / "deploy/domestic/bootstrap-host.sh",
        ):
            self.assertIn("lock-down-sshd.sh", script.read_text())

    def _apply_lockdown(self, script, dropin):
        env = os.environ.copy()
        env.update(TOKENKEY_SSH_LOCK_DRY="1", TOKENKEY_SSHD_DROPIN=str(dropin))
        return subprocess.run(
            ["bash", "-c", 'source "$1"; tokenkey_sshd_lockdown', "lookup", str(script)],
            env=env, text=True, capture_output=True,
        )

    def test_repo_checkout_and_runtime_bundle_both_apply_lockdown(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            repo = self._apply_lockdown(
                ROOT / "deploy/domestic/bootstrap-host.sh", tmp / "repo.conf",
            )
            self.assertEqual(repo.returncode, 0, repo.stderr)
            self.assertIn("PasswordAuthentication no", (tmp / "repo.conf").read_text())

            bundle = tmp / "runtime"
            bundle.mkdir()
            shutil.copy(ROOT / "deploy/domestic/bootstrap-host.sh", bundle / "bootstrap-host.sh")
            shutil.copy(LOCK, bundle / "lock-down-sshd.sh")
            bundled = self._apply_lockdown(bundle / "bootstrap-host.sh", tmp / "bundle.conf")
            self.assertEqual(bundled.returncode, 0, bundled.stderr)
            self.assertIn("PermitRootLogin no", (tmp / "bundle.conf").read_text())

            bare = tmp / "bare"
            bare.mkdir()
            shutil.copy(ROOT / "deploy/domestic/bootstrap-host.sh", bare / "bootstrap-host.sh")
            missing = self._apply_lockdown(bare / "bootstrap-host.sh", tmp / "bare.conf")
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn("missing lock-down-sshd.sh", missing.stderr)
            self.assertFalse((tmp / "bare.conf").exists())

    def test_prepare_runtime_ships_lockdown_beside_bootstrap(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            (state / "images.json").write_text(json.dumps({
                name: {"tag": f"fixture/{name}:verified"}
                for name in ("tokenkey", "caddy", "postgres", "redis")
            }))
            subprocess.check_call(
                [sys.executable, str(ROOT / "deploy/domestic/prepare-runtime.py"), str(state)],
                stdout=subprocess.DEVNULL,
            )
            bundled = state / "runtime" / "lock-down-sshd.sh"
            self.assertEqual(bundled.read_bytes(), LOCK.read_bytes())
            applied = self._apply_lockdown(
                state / "runtime" / "bootstrap-host.sh", state / "dropin.conf",
            )
            self.assertEqual(applied.returncode, 0, applied.stderr)
            self.assertIn("AllowTcpForwarding no", (state / "dropin.conf").read_text())


if __name__ == "__main__":
    unittest.main()
