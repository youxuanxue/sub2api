"""COS-over-S3 fetch: the guarantees that make a mirror trustworthy.

Why this path exists rather than rclone, and why node1 pulls rather than the
host pushing, is measured in cos_s3_fetch's module docstring.
"""
from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent


def _load(name: str):
    spec = importlib.util.spec_from_file_location(name, HERE / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    assert spec and spec.loader
    spec.loader.exec_module(module)
    return module


fetch = _load("cos_s3_fetch")

# The live prod repository, read from pgbackrest.conf on 2026-11-03.
BUCKET = "tokenkey-prod-backup-usw-1320330536"
PREFIX = "prod/postgres"
ENDPOINT = "cos.na-siliconvalley.myqcloud.com"
REGION = "na-siliconvalley"


class ReadOnlyContractTest(unittest.TestCase):
    """The credential carries write permission; this code must never use it."""

    def test_module_calls_no_mutating_s3_operation(self) -> None:
        source = (HERE / "cos_s3_fetch.py").read_text()
        for mutating in ("put_object", "delete_object", "delete_objects",
                         "copy_object", "create_bucket", "put_bucket",
                         "upload_file", "upload_fileobj", "abort_multipart"):
            with self.subTest(op=mutating):
                self.assertNotIn(mutating, source)

    def test_only_list_and_get_are_used(self) -> None:
        source = (HERE / "cos_s3_fetch.py").read_text()
        self.assertIn("list_objects_v2", source)
        self.assertIn("get_object", source)


class BucketAndPrefixTest(unittest.TestCase):
    def test_live_bucket_is_accepted(self) -> None:
        self.assertEqual(fetch.validate_bucket(BUCKET), BUCKET)

    def test_a_non_cos_bucket_name_is_refused(self) -> None:
        for bad in ("nope", "no-appid-suffix", "UPPER-123456", ""):
            with self.subTest(bucket=bad), self.assertRaises(fetch.FetchError):
                fetch.validate_bucket(bad)

    def test_prefix_is_normalised(self) -> None:
        self.assertEqual(fetch.normalise_prefix("/prod/postgres/"), PREFIX)
        self.assertEqual(fetch.normalise_prefix("prod//postgres"), PREFIX)

    def test_prefix_cannot_escape_upward(self) -> None:
        for bad in ("../other", "prod/../../etc", "prod/./postgres", "", "/"):
            with self.subTest(prefix=bad), self.assertRaises(fetch.FetchError):
                fetch.normalise_prefix(bad)


class ObjectListingTest(unittest.TestCase):
    def _client(self, pages):
        client = mock.MagicMock()
        client.list_objects_v2.side_effect = pages
        return client

    def test_pagination_is_followed(self) -> None:
        pages = [
            {"Contents": [{"Key": f"{PREFIX}/a", "Size": 10, "ETag": '"x"'}],
             "IsTruncated": True, "NextContinuationToken": "t1"},
            {"Contents": [{"Key": f"{PREFIX}/b", "Size": 20, "ETag": '"y"'}],
             "IsTruncated": False},
        ]
        objects = list(fetch.iter_objects(self._client(pages), BUCKET, PREFIX))
        self.assertEqual([o["key"] for o in objects], [f"{PREFIX}/a", f"{PREFIX}/b"])
        self.assertEqual(sum(o["bytes"] for o in objects), 30)

    def test_directory_placeholders_are_skipped(self) -> None:
        pages = [{"Contents": [{"Key": f"{PREFIX}/dir/", "Size": 0},
                               {"Key": f"{PREFIX}/real", "Size": 5}],
                  "IsTruncated": False}]
        objects = list(fetch.iter_objects(self._client(pages), BUCKET, PREFIX))
        self.assertEqual([o["key"] for o in objects], [f"{PREFIX}/real"])

    def test_truncated_page_without_a_token_stops(self) -> None:
        # A server that claims truncation but sends no token would otherwise
        # loop forever re-listing the first page.
        pages = [{"Contents": [{"Key": f"{PREFIX}/a", "Size": 1}], "IsTruncated": True}]
        objects = list(fetch.iter_objects(self._client(pages), BUCKET, PREFIX))
        self.assertEqual(len(objects), 1)


class TargetPathTest(unittest.TestCase):
    def test_keys_land_under_staging(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root)
            target = fetch._target_for(staging, f"{PREFIX}/backup/20261101F/x.gz", PREFIX)
            self.assertEqual(target, (staging / "backup/20261101F/x.gz").resolve())

    def test_a_key_escaping_staging_is_refused(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root) / "s"
            staging.mkdir()
            for bad in (f"{PREFIX}/../../etc/passwd", f"{PREFIX}/"):
                with self.subTest(key=bad), self.assertRaises(fetch.FetchError):
                    fetch._target_for(staging, bad, PREFIX)


class DownloadTest(unittest.TestCase):
    def _body(self, payload: bytes):
        body = mock.MagicMock()
        chunks = [payload, b""]
        body.read.side_effect = lambda *_: chunks.pop(0) if chunks else b""
        return body

    def test_a_complete_object_is_renamed_into_place(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root)
            client = mock.MagicMock()
            client.get_object.return_value = {"Body": self._body(b"hello")}
            written = fetch._download_one(client, BUCKET, PREFIX, staging,
                                          {"key": f"{PREFIX}/f.gz", "bytes": 5})
            self.assertEqual(written, 5)
            self.assertEqual((staging / "f.gz").read_bytes(), b"hello")
            self.assertEqual(list(staging.glob("*.part")), [],
                             "a .part file must not survive a successful fetch")

    def test_a_short_read_leaves_no_file(self) -> None:
        # The byte count is the cheap guard that keeps a truncated object out of
        # the manifest stage; without it a half-written backup would be hashed
        # and published as complete.
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root)
            client = mock.MagicMock()
            client.get_object.return_value = {"Body": self._body(b"short")}
            with self.assertRaises(fetch.FetchError) as caught:
                fetch._download_one(client, BUCKET, PREFIX, staging,
                                    {"key": f"{PREFIX}/f.gz", "bytes": 500})
            self.assertIn("short read", str(caught.exception))
            self.assertEqual(list(staging.iterdir()), [])

    def test_fetch_refuses_an_empty_prefix(self) -> None:
        with tempfile.TemporaryDirectory() as root, \
             mock.patch.object(fetch, "_client", return_value=mock.MagicMock()):
            with self.assertRaises(fetch.FetchError) as caught:
                fetch.fetch_prefix(bucket=BUCKET, prefix=PREFIX, endpoint=ENDPOINT,
                                   region=REGION, staging=pathlib.Path(root), objects=[])
            self.assertIn("no objects", str(caught.exception))

    def test_a_failed_object_fails_the_whole_fetch(self) -> None:
        with tempfile.TemporaryDirectory() as root, \
             mock.patch.object(fetch, "_client", return_value=mock.MagicMock()), \
             mock.patch.object(fetch, "_download_one", side_effect=OSError("reset")):
            with self.assertRaises(fetch.FetchError) as caught:
                fetch.fetch_prefix(bucket=BUCKET, prefix=PREFIX, endpoint=ENDPOINT,
                                   region=REGION, staging=pathlib.Path(root),
                                   objects=[{"key": f"{PREFIX}/a", "bytes": 1}])
            self.assertIn("failed to download", str(caught.exception))


class ConcurrencyTest(unittest.TestCase):
    def test_default_matches_the_measured_optimum(self) -> None:
        # 8 workers gave 19.9 MiB/s and 16 gave 29.7 MiB/s on node1 against the
        # live repository; a single stream managed 2.9. Dropping back to a
        # serial fetch would turn an 8-minute first sync into 84 minutes.
        self.assertGreaterEqual(fetch.MAX_WORKERS, 16)

    def test_a_zero_worker_request_still_runs(self) -> None:
        with tempfile.TemporaryDirectory() as root, \
             mock.patch.object(fetch, "_client", return_value=mock.MagicMock()), \
             mock.patch.object(fetch, "_download_one", return_value=4):
            result = fetch.fetch_prefix(bucket=BUCKET, prefix=PREFIX, endpoint=ENDPOINT,
                                        region=REGION, staging=pathlib.Path(root),
                                        objects=[{"key": f"{PREFIX}/a", "bytes": 4}],
                                        workers=0)
        self.assertEqual(result["object_count"], 1)


class CredentialTest(unittest.TestCase):
    def test_missing_credentials_fail_before_any_request(self) -> None:
        try:
            import boto3  # noqa: F401
        except ImportError:
            self.skipTest("boto3 not installed here; node1 carries 1.34.46")
        blank = {name: "" for name in fetch.ENV_KEY_ALIASES + fetch.ENV_SECRET_ALIASES}
        with mock.patch.dict(fetch.os.environ, blank, clear=False):
            with self.assertRaises(fetch.FetchError) as caught:
                fetch._client(ENDPOINT, REGION)
            self.assertIn(fetch.ENV_KEY, str(caught.exception))

    def test_a_missing_boto3_is_reported_clearly(self) -> None:
        # The fetch runs on node1, so a host without boto3 must say so rather
        # than fail somewhere deeper.
        with mock.patch.dict("sys.modules", {"boto3": None}):
            with self.assertRaises(fetch.FetchError) as caught:
                fetch._client(ENDPOINT, REGION)
            self.assertIn("boto3", str(caught.exception))

    def test_the_hosts_own_key_names_are_accepted(self) -> None:
        # /etc/tokenkey/cos-backup.env spells them tencent_SecretId /
        # tencent_SecretKey; assuming COS_SECRET_ID silently read nothing.
        self.assertEqual(fetch.ENV_KEY, "tencent_SecretId")
        self.assertEqual(fetch.ENV_SECRET, "tencent_SecretKey")
        with mock.patch.dict(fetch.os.environ,
                             {"tencent_SecretId": "k", "tencent_SecretKey": "s"},
                             clear=False):
            self.assertEqual(fetch._from_env(fetch.ENV_KEY_ALIASES), "k")
            self.assertEqual(fetch._from_env(fetch.ENV_SECRET_ALIASES), "s")

    def test_credentials_are_never_hardcoded(self) -> None:
        source = (HERE / "cos_s3_fetch.py").read_text()
        self.assertNotIn("AKID", source)
        self.assertRegex(source, r"os\.environ\.get\(name")
        self.assertIn("_from_env(ENV_KEY_ALIASES)", source)


if __name__ == "__main__":
    unittest.main()


class MirrorIntegrationTest(unittest.TestCase):
    """How the S3 backend plugs into cos_node1_mirror."""

    def setUp(self) -> None:
        self.mirror = _load("cos_node1_mirror")

    def test_s3_coordinates_are_not_mutually_exclusive(self) -> None:
        # Putting --s3-prefix in the source group made argparse reject
        # "--s3-bucket X --s3-prefix Y" outright: the live plan run failed with
        # "not allowed with argument --s3-bucket" while --apply worked.
        args = self.mirror.parse_args([
            "copy", "--namespace", "db", "--snapshot", "s",
            "--s3-bucket", BUCKET, "--s3-prefix", PREFIX,
            "--s3-endpoint", ENDPOINT, "--s3-region", REGION,
            "--destination", "/tmp/x"])
        self.assertEqual(args.s3_bucket, BUCKET)
        self.assertEqual(args.s3_prefix, PREFIX)

    def test_rclone_and_s3_remain_mutually_exclusive(self) -> None:
        import contextlib, io
        with self.assertRaises(SystemExit), contextlib.redirect_stderr(io.StringIO()):
            self.mirror.parse_args([
                "copy", "--namespace", "db", "--snapshot", "s",
                "--s3-bucket", BUCKET, "--source-uri", "cos:x",
                "--destination", "/tmp/x"])

    def test_incomplete_s3_coordinates_are_refused(self) -> None:
        args = self.mirror.parse_args([
            "copy", "--namespace", "db", "--snapshot", "s",
            "--s3-bucket", BUCKET, "--destination", "/tmp/x"])
        with self.assertRaises(self.mirror.MirrorError) as caught:
            self.mirror.require_s3_args(args)
        self.assertIn("--s3-prefix", str(caught.exception))

    def test_receipt_records_which_bucket_a_snapshot_came_from(self) -> None:
        # An S3 fetch left `source` as None, so a published snapshot carried no
        # record of its origin -- the one fact needed when choosing which copy
        # to restore from.
        args = self.mirror.parse_args([
            "copy", "--namespace", "db", "--snapshot", "s",
            "--s3-bucket", BUCKET, "--s3-prefix", PREFIX,
            "--s3-endpoint", ENDPOINT, "--s3-region", REGION,
            "--destination", "/tmp/x"])
        described = self.mirror.describe_source(args)
        self.assertIn(BUCKET, described)
        self.assertIn(PREFIX, described)
        self.assertIn(ENDPOINT, described)

    def test_plan_prices_the_prefix_instead_of_demanding_a_byte_count(self) -> None:
        # plan refused a command that --apply accepted, which is the opposite
        # of what a dry run is for.
        source = (HERE / "cos_node1_mirror.py").read_text()
        plan = source[source.index("def plan_snapshot"):]
        plan = plan[:plan.index("\ndef ", 1)] if "\ndef " in plan[1:] else plan
        self.assertIn("cos_s3_fetch.survey", plan,
                      "plan must survey the prefix like copy does")
