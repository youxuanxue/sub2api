import copy
import hashlib
import io
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from email.utils import format_datetime
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qsl, urlencode

from ops.qa.migrate_hcloud_objects import (
    SOURCES,
    copy_one,
    expiry,
    frozen_inventory,
    install_lifecycle,
    lifecycle_rules,
    ledger_open,
    verify_frozen,
    verify_one,
)


class Missing(Exception):
    response = {"ResponseMetadata": {"HTTPStatusCode": 404}}


class Store:
    def __init__(self):
        self.objects, self.uploads = {}, {}
        self.rules = []
        self.writes = 0
        self.fail_read = False

    def get_bucket_lifecycle_configuration(self, Bucket):
        return {"Rules": copy.deepcopy(self.rules)}

    def put_bucket_lifecycle_configuration(self, Bucket, LifecycleConfiguration):
        self.rules = copy.deepcopy(LifecycleConfiguration["Rules"])

    def seed(self, key, body, **headers):
        self.objects[key] = dict(
            headers,
            Body=body,
            ContentLength=len(body),
            ETag='"' + hashlib.md5(body).hexdigest() + '"',
        )

    def get_paginator(self, name):
        store = self

        class Paginator:
            def paginate(self, Bucket, Prefix):
                yield {
                    "Contents": [
                        {
                            "Key": key,
                            "Size": value["ContentLength"],
                            "ETag": value["ETag"],
                        }
                        for key, value in store.objects.items()
                        if key.startswith(Prefix)
                    ]
                }

        return Paginator()

    def head_object(self, Bucket, Key):
        if Key not in self.objects:
            raise Missing()
        return {
            k: copy.deepcopy(v) for k, v in self.objects[Key].items() if k != "Body"
        }

    def get_object_tagging(self, Bucket, Key):
        return {
            "TagSet": [
                {"Key": k, "Value": v}
                for k, v in parse_qsl(self.objects[Key].get("Tagging", ""))
            ]
        }

    def put_object_tagging(self, Bucket, Key, Tagging):
        self.objects[Key]["Tagging"] = urlencode(
            {t["Key"]: t["Value"] for t in Tagging["TagSet"]}
        )

    def get_object(self, Bucket, Key, IfMatch):
        if self.fail_read:
            raise RuntimeError("synthetic read interruption")
        value = self.head_object(Bucket, Key)
        if value["ETag"] != IfMatch:
            raise ValueError("source/target version changed")
        return dict(value, Body=io.BytesIO(self.objects[Key]["Body"]))

    def create_multipart_upload(self, **fields):
        self.uploads["upload"] = dict(fields, Parts={})
        return {"UploadId": "upload"}

    def upload_part(self, Bucket, Key, UploadId, PartNumber, Body):
        self.uploads[UploadId]["Parts"][PartNumber] = Body
        return {"ETag": str(PartNumber)}

    def complete_multipart_upload(
        self, Bucket, Key, UploadId, MultipartUpload, IfMatch=None, IfNoneMatch=None
    ):
        if IfNoneMatch == "*" and Key in self.objects:
            raise ValueError("create conflict")
        if IfMatch and self.objects[Key]["ETag"].strip('"') != IfMatch:
            raise ValueError("replace conflict")
        upload = self.uploads.pop(UploadId)
        headers = {
            k: v
            for k, v in upload.items()
            if k not in ("Parts", "Key", "Bucket", "Tagging")
        }
        self.seed(Key, b"".join(upload["Parts"].values()), **headers)
        self.writes += 1
        return {"ETag": self.objects[Key]["ETag"]}

    def abort_multipart_upload(self, Bucket, Key, UploadId):
        self.uploads.pop(UploadId, None)


class LifecycleStore:
    def __init__(self, rules=None):
        self.rules = rules or []

    def get_bucket_lifecycle_configuration(self, Bucket):
        return {"Rules": self.rules}

    def put_bucket_lifecycle_configuration(self, Bucket, LifecycleConfiguration):
        self.rules = LifecycleConfiguration["Rules"]


class LifecycleTest(unittest.TestCase):
    def test_absolute_expiry_and_default_retention_survive_incremental_copy(self):
        target = LifecycleStore()
        install_lifecycle(target, "private-target", "raw", ["2026-10-12"])
        install_lifecycle(target, "private-target", "raw", ["2026-10-13"])
        actual = {r["ID"]: r for r in target.rules}
        self.assertEqual(actual["expire-qa-runtime-objects"]["Expiration"], {"Days": 7})
        for day in ("2026-10-12", "2026-10-13"):
            rule = actual["migration-original-expiry-" + day]
            self.assertEqual(
                rule["Expiration"]["Date"].isoformat(), day + "T00:00:00+00:00"
            )
            self.assertEqual(
                rule["Filter"], {"Tag": {"Key": "tk-original-expiry", "Value": day}}
            )

    def test_foreign_rules_are_not_overwritten(self):
        rules = [{"ID": "operator-owned", "Status": "Enabled"}]
        target = LifecycleStore(copy.deepcopy(rules))
        with self.assertRaises(ValueError):
            install_lifecycle(target, "private-target", "raw", ["2026-10-12"])
        self.assertEqual(target.rules, rules)


class MigrationObjectsTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.db = ledger_open(Path(self.directory.name, "ledger.sqlite"))
        self.source, self.target = Store(), Store()
        self.key = "raw/v1/2026/10/11/00/commit.json"
        self.deadline = datetime.now(timezone.utc).replace(
            hour=0, minute=0, second=0, microsecond=0
        ) + timedelta(days=1)
        self.headers = {
            "Expiration": f'expiry-date="{format_datetime(self.deadline, usegmt=True)}", rule-id="raw"',
            "ContentType": "application/json",
            "ContentEncoding": "gzip",
            "Metadata": {"test": "synthetic"},
        }
        self.source.seed(self.key, b"synthetic QA contents", **self.headers)
        install_lifecycle(
            self.target, SOURCES["raw"][1], "raw", [self.deadline.date().isoformat()]
        )

    def tearDown(self):
        self.db.close()
        self.directory.cleanup()

    def copy(self):
        return copy_one(
            self.source,
            self.target,
            self.db,
            SOURCES["raw"][0],
            SOURCES["raw"][1],
            self.key,
            float("inf"),
        )

    def test_copy_roundtrip_preserves_bytes_headers_expiry_and_skips_verified(self):
        self.assertEqual(self.copy(), "copied")
        target = self.target.objects[self.key]
        self.assertEqual(target["Body"], self.source.objects[self.key]["Body"])
        self.assertEqual(target["ContentEncoding"], "gzip")
        self.assertEqual(
            target["Metadata"]["tk-migration-expires-at"], self.deadline.isoformat()
        )
        self.assertEqual(self.copy(), "verified")
        self.assertEqual(self.target.writes, 1)
        row = self.db.execute("SELECT sha256,size FROM objects").fetchone()
        self.assertEqual(
            row, (hashlib.sha256(target["Body"]).hexdigest(), len(target["Body"]))
        )

    def test_final_verification_ignores_expired_objects_pending_source_deletion(self):
        self.copy()
        expired_key = "raw/v1/expired/commit.json"
        expired_headers = dict(self.headers)
        deadline = self.deadline - timedelta(days=2)
        expired_headers["Expiration"] = (
            f'expiry-date="{format_datetime(deadline, usegmt=True)}", rule-id="raw"'
        )
        self.source.seed(expired_key, b"expired QA contents", **expired_headers)
        install_lifecycle(
            self.target, SOURCES["raw"][1], "raw", [deadline.date().isoformat()]
        )
        self.assertEqual(
            copy_one(
                self.source,
                self.target,
                self.db,
                *SOURCES["raw"][:2],
                expired_key,
                float("inf"),
            ),
            "expired",
        )
        inventory = frozen_inventory(self.source, SOURCES["raw"][0], "raw/v1/")
        # S3 lifecycle deletion may finish after the inventory was frozen.
        del self.source.objects[expired_key]
        result = verify_frozen(
            self.source, self.target, self.db, *SOURCES["raw"], inventory, float("inf")
        )
        self.assertEqual(
            result,
            [
                {
                    "key": self.key,
                    "size": len(b"synthetic QA contents"),
                    "sha256": hashlib.sha256(b"synthetic QA contents").hexdigest(),
                }
            ],
        )
        self.assertEqual(self.target.writes, 1)

    def test_expiry_during_frozen_verification_requires_a_new_inventory(self):
        self.copy()
        inventory = frozen_inventory(self.source, SOURCES["raw"][0], "raw/v1/")
        with patch("ops.qa.migrate_hcloud_objects.datetime", wraps=datetime) as clock:
            clock.now.side_effect = [
                datetime.now(timezone.utc), self.deadline + timedelta(seconds=1)
            ]
            with self.assertRaisesRegex(ValueError, "source inventory changed"):
                verify_frozen(
                    self.source,
                    self.target,
                    self.db,
                    *SOURCES["raw"],
                    inventory,
                    float("inf"),
                )

    def test_final_verification_rereads_bytes_even_when_etag_and_ledger_match(self):
        self.copy()
        inventory = frozen_inventory(self.source, SOURCES["raw"][0], "raw/v1/")
        result = verify_frozen(
            self.source, self.target, self.db, *SOURCES["raw"], inventory, float("inf")
        )
        self.assertEqual(
            result,
            [
                {
                    "key": self.key,
                    "size": len(b"synthetic QA contents"),
                    "sha256": hashlib.sha256(b"synthetic QA contents").hexdigest(),
                }
            ],
        )
        self.target.objects[self.key]["Body"] = b"X" * len(b"synthetic QA contents")
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            verify_one(
                self.source,
                self.target,
                self.db,
                *SOURCES["raw"][:2],
                inventory[0],
                float("inf"),
            )
        self.assertEqual(self.target.writes, 1)

    def test_final_verification_rejects_new_objects_and_source_drift(self):
        self.copy()
        inventory = frozen_inventory(self.source, SOURCES["raw"][0], "raw/v1/")
        self.source.seed("raw/v1/new.json", b"new", **self.headers)
        with self.assertRaisesRegex(ValueError, "inventory changed"):
            verify_frozen(
                self.source,
                self.target,
                self.db,
                *SOURCES["raw"],
                inventory,
                float("inf"),
            )
        del self.source.objects["raw/v1/new.json"]
        self.source.seed(self.key, b"drift", **self.headers)
        with self.assertRaisesRegex(ValueError, "frozen inventory"):
            verify_one(
                self.source,
                self.target,
                self.db,
                *SOURCES["raw"][:2],
                inventory[0],
                float("inf"),
            )

    def test_final_verification_rejects_missing_disabled_or_changed_lifecycle(self):
        self.copy()
        inventory = frozen_inventory(self.source, SOURCES["raw"][0], "raw/v1/")
        original = copy.deepcopy(self.target.rules)
        for mutation in ("missing", "disabled", "expiry", "runtime"):
            with self.subTest(mutation=mutation):
                self.target.rules = copy.deepcopy(original)
                if mutation == "missing":
                    self.target.rules.pop()
                elif mutation == "disabled":
                    self.target.rules[-1]["Status"] = "Disabled"
                elif mutation == "expiry":
                    self.target.rules[-1]["Expiration"]["Date"] += timedelta(days=1)
                else:
                    self.target.rules[0]["Expiration"]["Days"] = 30
                with self.assertRaisesRegex(ValueError, "lifecycle"):
                    verify_frozen(
                        self.source,
                        self.target,
                        self.db,
                        *SOURCES["raw"],
                        inventory,
                        float("inf"),
                    )
        self.assertEqual(self.target.writes, 1)

    def test_final_verification_rejects_lifecycle_change_during_readback(self):
        self.copy()
        inventory = frozen_inventory(self.source, SOURCES["raw"][0], "raw/v1/")
        original = self.target.get_object

        rules = copy.deepcopy(self.target.rules)
        for mutation in ("disabled", "additional_date"):
            with self.subTest(mutation=mutation):
                self.target.rules = copy.deepcopy(rules)

                def changed(*args, **kwargs):
                    if mutation == "disabled":
                        self.target.rules[-1]["Status"] = "Disabled"
                    else:
                        day = (self.deadline + timedelta(days=1)).date().isoformat()
                        self.target.rules.append(lifecycle_rules("raw", [day])[-1])
                    return original(*args, **kwargs)

                with patch.object(self.target, "get_object", side_effect=changed):
                    with self.assertRaisesRegex(ValueError, "lifecycle"):
                        verify_frozen(
                            self.source,
                            self.target,
                            self.db,
                            *SOURCES["raw"],
                            inventory,
                            float("inf"),
                        )
        self.assertEqual(self.target.writes, 1)

    def test_source_tags_and_absolute_expiry_are_restored_after_multipart_tag_loss(
        self,
    ):
        self.source.objects[self.key]["Tagging"] = "purpose=ordinary-qa"
        self.assertEqual(self.copy(), "copied")
        tags = self.target.get_object_tagging("target", self.key)["TagSet"]
        self.assertEqual(
            {t["Key"]: t["Value"] for t in tags},
            {
                "purpose": "ordinary-qa",
                "tk-original-expiry": self.deadline.date().isoformat(),
            },
        )
        self.source.seed(
            self.key,
            b"next-day commit",
            **dict(
                self.headers,
                Expiration=f'expiry-date="{format_datetime(self.deadline + timedelta(days=1), usegmt=True)}"',
            ),
        )
        self.assertEqual(self.copy(), "copied")
        self.assertEqual(
            dict(parse_qsl(self.target.objects[self.key]["Tagging"]))[
                "tk-original-expiry"
            ],
            (self.deadline + timedelta(days=1)).date().isoformat(),
        )

    def test_interrupted_readback_recovers_without_overwriting(self):
        self.target.fail_read = True
        with self.assertRaises(RuntimeError):
            self.copy()
        self.assertEqual(
            self.db.execute("SELECT count(*) FROM objects").fetchone()[0], 0
        )
        self.target.fail_read = False
        self.assertEqual(self.copy(), "recovered")
        self.assertEqual(self.target.writes, 1)

    def test_recovery_rejects_corruption_and_unknown_target(self):
        self.target.seed(self.key, b"unknown foreign object")
        with self.assertRaisesRegex(ValueError, "not owned"):
            self.copy()
        self.target.objects.clear()
        self.target.fail_read = True
        with self.assertRaises(RuntimeError):
            self.copy()
        self.target.fail_read = False
        row = self.target.objects[self.key]
        row["Body"] = b"X" * len(row["Body"])
        with self.assertRaisesRegex(ValueError, "differs"):
            self.copy()
        self.assertEqual(self.target.writes, 1)

    def test_only_owned_mutable_commit_may_be_replaced(self):
        self.assertEqual(self.copy(), "copied")
        self.source.seed(self.key, b"next generation", **self.headers)
        self.assertEqual(self.copy(), "copied")
        self.key = "raw/v1/2026/10/11/00/records.parquet"
        self.source.seed(self.key, b"immutable", **self.headers)
        self.assertEqual(self.copy(), "copied")
        self.source.seed(self.key, b"changed immutable", **self.headers)
        with self.assertRaisesRegex(ValueError, "immutable"):
            self.copy()

    def test_expired_missing_expiry_and_changed_source_never_publish(self):
        with self.assertRaises(ValueError):
            expiry({})
        self.source.objects[self.key]["Expiration"] = (
            'expiry-date="Thu, 01 Jan 1970 00:00:00 GMT"'
        )
        self.assertEqual(self.copy(), "expired")
        self.assertEqual(self.target.writes, 0)
        self.source.objects[self.key]["Expiration"] = self.headers["Expiration"]
        original = self.source.head_object
        calls = 0

        def changed(*args, **kwargs):
            nonlocal calls
            calls += 1
            value = original(*args, **kwargs)
            if calls > 2:
                value["ETag"] = "changed"
            return value

        with patch.object(self.source, "head_object", side_effect=changed):
            with self.assertRaisesRegex(ValueError, "source changed"):
                self.copy()
        self.assertEqual(self.target.writes, 0)
        self.assertEqual(self.target.uploads, {})


if __name__ == "__main__":
    unittest.main()
