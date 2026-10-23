"""Execute the audit query against active, dormant, granted and deleted fixtures."""
import pathlib
import sqlite3
import unittest


class ExclusiveOrphansTest(unittest.TestCase):
    def test_only_active_ungranted_standard_keys_are_actionable(self):
        with sqlite3.connect(':memory:') as db:
            db.executescript('''
              CREATE TABLE api_keys(id, user_id, group_id, status, deleted_at);
              CREATE TABLE users(id, email, status, deleted_at);
              CREATE TABLE groups(id, name, status, deleted_at, is_exclusive, subscription_type);
              CREATE TABLE user_allowed_groups(user_id, group_id);
              INSERT INTO users VALUES(1,'active','active',NULL),(2,'disabled','disabled',NULL);
              INSERT INTO groups VALUES(1,'private','active',NULL,true,'standard'),
                (2,'probe','disabled',NULL,true,'standard'),(3,'subscription','active',NULL,true,'subscription'),
                (4,'public','active',NULL,false,'standard');
              INSERT INTO api_keys VALUES(1,1,1,'active',NULL),(2,1,1,'disabled',NULL),
                (3,1,2,'active',NULL),(4,2,1,'active',NULL),(5,1,3,'active',NULL),
                (6,1,4,'active',NULL),(7,1,1,'active','deleted');
            ''')
            sql = pathlib.Path(__file__).with_name('exclusive_group_orphans.sql').read_text()
            self.assertEqual([r[0] for r in db.execute(sql)],
                             ['orphans=1', 'dormant_orphans=3', 'orphan_detail|1|1|active|1|private'])
            db.execute('INSERT INTO user_allowed_groups VALUES(1,1)')
            self.assertEqual([r[0] for r in db.execute(sql)], ['orphans=0', 'dormant_orphans=2'])


class ExclusiveOrphanTransportTest(unittest.TestCase):
    def test_remote_command_uses_sudo_docker_resolver_under_bash(self):
        import json
        import os
        import subprocess
        import tempfile

        script = pathlib.Path(__file__).with_name("check_exclusive_group_orphans_via_ssm.sh")
        with tempfile.TemporaryDirectory() as tmp:
            env = os.environ.copy()
            env["EXCLUSIVE_GROUP_RENDER_ONLY"] = "1"
            env["EXCLUSIVE_GROUP_RENDER_DIR"] = tmp
            proc = subprocess.run(
                ["bash", str(script), "mi-stub"],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            payload = json.loads((pathlib.Path(tmp) / "ssm-params.json").read_text())
            wrapped = payload["commands"]
            self.assertEqual(len(wrapped), 1)
            self.assertIn("base64 -d | bash -s", wrapped[0])
            import base64
            body = base64.b64decode(wrapped[0].split()[1]).decode()
            self.assertIn("set -euo pipefail", body)
            self.assertIn("sudo docker inspect", body)
            self.assertIn("sudo docker exec", body)
            self.assertIn("tk-archive-standby", body)
            self.assertNotIn("docker exec -i tokenkey-postgres", body)


if __name__ == '__main__':
    unittest.main()
