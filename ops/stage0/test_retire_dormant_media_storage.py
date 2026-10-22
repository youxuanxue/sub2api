#!/usr/bin/env python3
import json
import os
import pathlib
import tempfile
import unittest
from unittest import mock

import retire_dormant_media_storage as media
from retire_dormant_media_storage import clean, LEGACY

ROOT = pathlib.Path(__file__).resolve().parents[2]


class MediaCleanupTests(unittest.TestCase):
    def test_remove_only_known_dormant_defaults_preserving_other_secrets_and_flags(self):
        unrelated = "JWT_SECRET=synthetic-secret\nIMAGE_STORAGE_ENABLED=false\nMEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED=false\n"
        old = unrelated + "\n".join(f"{key}={value}" for key, value in LEGACY.items()) + "\n"
        self.assertEqual(clean(old), unrelated)
        self.assertEqual(clean(clean(old)), unrelated)

    def test_enabled_offload_custom_bucket_and_partial_config_require_review(self):
        for text in ["MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED=true\n",
                     "MEDIA_STORAGE_BUCKET=custom\n", "MEDIA_STORAGE_DRIVER=s3\n"]:
            with self.subTest(text=text):
                with self.assertRaises(RuntimeError):
                    clean(text)


class ProdIdentityTests(unittest.TestCase):
    def test_identity_comes_from_live_tencent_matrix_not_retired_hetzner(self):
        matrix = json.loads((ROOT / "deploy/tencent/candidate-usw-20261022.json").read_text())
        with mock.patch.dict(os.environ, {"TK_TENCENT_PROD_MATRIX": ""}):
            self.assertEqual(media.prod_identity(), matrix["target"]["hostname"])
        self.assertNotIn("prod-target-hetzner", pathlib.Path(media.__file__).read_text())

    def test_missing_or_non_live_matrix_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "m.json"
            with self.assertRaisesRegex(RuntimeError, "prod_matrix_missing"):
                media.prod_identity([path])
            path.write_text(json.dumps({"target": {"deployable": False, "hostname": "h"}}))
            with self.assertRaisesRegex(RuntimeError, "prod_matrix_not_live"):
                media.prod_identity([path])

    def test_plan_uses_resolved_tencent_data_containers(self):
        calls = []

        def run(args, **_kwargs):
            calls.append(args)
            if args[:2] == ["docker", "inspect"]:
                return json.dumps(["MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED=false", "REDIS_DB=0"])
            if "psql" in args:
                return "0"
            return ""

        with tempfile.TemporaryDirectory() as tmp:
            env_file = pathlib.Path(tmp) / ".env"
            env_file.write_text("JWT_SECRET=x\n")
            with mock.patch.dict(os.environ, {"APPLY": ""}), \
                 mock.patch.object(media, "prod_identity", return_value="VM-0-16-ubuntu"), \
                 mock.patch.object(media.socket, "gethostname", return_value="VM-0-16-ubuntu"), \
                 mock.patch.object(media, "resolve", return_value=("tokenkey-blue", [])), \
                 mock.patch.object(media, "resolve_postgres", return_value="tk-archive-standby"), \
                 mock.patch.object(media, "resolve_redis", return_value="tk-cutover-prod-redis"), \
                 mock.patch.object(media, "run", side_effect=run), \
                 mock.patch.object(media, "Path", side_effect=lambda p: env_file if p == "/var/lib/tokenkey/.env" else pathlib.Path(p)), \
                 mock.patch("sys.argv", ["retire"]):
                media.main()
        execs = [c for c in calls if c[:2] == ["docker", "exec"]]
        self.assertTrue(any("tk-archive-standby" in c for c in execs))
        self.assertTrue(any("tk-cutover-prod-redis" in c for c in execs))
        self.assertFalse(any("tokenkey-postgres" in c or "tokenkey-redis" in c for c in execs))

    def test_unresolved_data_container_fails_before_any_exec(self):
        with mock.patch.dict(os.environ, {"APPLY": ""}), \
             mock.patch.object(media, "prod_identity", return_value="h"), \
             mock.patch.object(media.socket, "gethostname", return_value="h"), \
             mock.patch.object(media, "resolve", return_value=("tokenkey-blue", [])), \
             mock.patch.object(media, "resolve_postgres", return_value=None), \
             mock.patch.object(media, "resolve_redis", return_value="tk-cutover-prod-redis"), \
             mock.patch.object(media, "run") as run, \
             mock.patch("sys.argv", ["retire"]):
            with self.assertRaisesRegex(RuntimeError, "data_container_unresolved"):
                media.main()
        run.assert_not_called()

    def test_registered_contract_ships_matrix(self):
        contracts = json.loads((ROOT / "ops/observability/probe-contracts.json").read_text())
        companions = contracts["retire_dormant_media_storage_remote.sh"]["companions"]
        self.assertIn("deploy/tencent/candidate-usw-20261022.json", companions)
        self.assertIn("ops/stage0/retire_dormant_media_storage.py", companions)


if __name__ == "__main__":
    unittest.main()
