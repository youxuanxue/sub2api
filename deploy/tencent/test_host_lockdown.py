import os
import shutil
import subprocess
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
        self.assertIn("--dport 22 -j DROP", rules)
        self.assertIn("--dport 8080 -j DROP", rules)
        self.assertNotIn("0.0.0.0/0", rules)
        for script in (
            ROOT / "deploy/tencent/bootstrap-prod-app.sh",
            ROOT / "deploy/tencent/install-cn-socks-proxy.sh",
            ROOT / "deploy/domestic/bootstrap-host.sh",
        ):
            self.assertIn("lock-down-sshd.sh", script.read_text())


if __name__ == "__main__":
    unittest.main()
