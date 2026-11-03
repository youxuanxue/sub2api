"""QA node1 remote push: the guarantees that decide whether an hour is safe.

Numbers in these tests are the ones measured on the live hosts 2026-11-03, so a
change that weakens a check fails against data that actually occurred.
"""
from __future__ import annotations

import datetime as dt
import importlib.util
import json
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


remote = _load("node1_remote")
archive = _load("node1_archive")

HOUR = dt.datetime(2026, 11, 2, 1, tzinfo=dt.timezone.utc)
# /storage on node1: 10T cold disk, 9.6T free, QA cap 6 TiB.
NODE1_TOTAL = 10_666_332_323_840
NODE1_FREE = 10_555_000_000_000


class SshRoutingTest(unittest.TestCase):
    def test_jump_connection_carries_the_key_explicitly(self) -> None:
        # `-J host` does NOT reuse `-i` for the jump hop. Without spelling the
        # proxy out, the jump authenticates as the invoking user and fails with
        # "Permission denied (publickey)" -- observed on the first live attempt.
        argv = remote.ssh_command(pathlib.Path("/etc/tokenkey/node1-archive.key"),
                                  "root@180.76.181.161", "root@10.10.2.11")
        proxy = next(a for a in argv if a.startswith("ProxyCommand="))
        self.assertIn("-i /etc/tokenkey/node1-archive.key", proxy)
        self.assertIn("root@180.76.181.161", proxy)
        self.assertIn("-W %h:%p", proxy)
        self.assertNotIn("-J", argv, "-J would drop the key on the jump hop")
        self.assertIn("root@10.10.2.11", argv)
        self.assertIn("IdentitiesOnly=yes", argv)

    def test_no_interactive_or_agent_forwarding(self) -> None:
        argv = remote.ssh_command(pathlib.Path("/k"), "root@j", "root@t")
        self.assertIn("BatchMode=yes", argv)
        for forbidden in ("-A", "-t", "-X", "-L", "-R", "-D"):
            self.assertNotIn(forbidden, argv)


class RemoteScriptTest(unittest.TestCase):
    def test_publishes_by_rename_from_a_staging_dir(self) -> None:
        script = remote._remote_script("/storage/tokenkey-archive/qa-raw/prod/2026/11/02/01")
        self.assertIn("mktemp -d", script)
        self.assertIn('mv "$staging" "$dir"', script)
        self.assertIn("trap cleanup EXIT", script,
                      "a failed unpack must not leave a partial directory")

    def test_recomputes_the_manifest_digest(self) -> None:
        # Exit code alone cannot distinguish a complete archive from a
        # truncated stream, so the remote side hashes manifest.json itself.
        script = remote._remote_script("/d")
        self.assertIn('sha256sum "$staging/manifest.json"', script)
        self.assertIn("manifest.json missing from stream", script)

    def test_refuses_to_overwrite_a_different_existing_hour(self) -> None:
        script = remote._remote_script("/d")
        self.assertIn("target exists with a different manifest", script)
        self.assertIn("already_present", script,
                      "an identical re-push must be idempotent, not an error")

    def test_remote_dir_is_shell_quoted(self) -> None:
        # shlex.quote keeps a hostile path as one literal argument. The payload
        # text still appears -- inside quotes, where the shell cannot run it --
        # so assert the quoting itself rather than the absence of the text.
        import shlex
        hostile = "/storage/a b/'; rm -rf /"
        script = remote._remote_script(hostile)
        self.assertIn(f"dir={shlex.quote(hostile)}", script)
        self.assertNotIn("\ndir=/storage/a b", script, "an unquoted path would split")


class StreamTest(unittest.TestCase):
    def test_tar_is_throttled_and_uncompressed(self) -> None:
        # QA files are already zstd; re-compressing measured 0% gain, so the
        # stream must stay raw. nice/ionice keep it behind the live gateway.
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root) / "s"
            staging.mkdir()
            calls = []

            def fake_popen(argv, **kwargs):
                calls.append(argv)
                handle = mock.MagicMock()
                handle.stdout = mock.MagicMock()
                handle.stderr = mock.MagicMock()
                handle.stderr.read.return_value = b""
                handle.returncode = 0
                handle.wait.return_value = 0
                return handle

            summary = {"ok": True, "manifest_sha256": "abc", "file_count": 2,
                       "total_bytes": 9, "already_present": False}
            with mock.patch.object(remote.subprocess, "Popen", side_effect=fake_popen), \
                 mock.patch.object(remote.subprocess, "run",
                                   return_value=mock.Mock(returncode=0, stdout=json.dumps(summary),
                                                          stderr="")), \
                 mock.patch.object(remote, "_has_pv", return_value=False):
                result = remote.push_directory(staging, key=pathlib.Path("/k"),
                                               jump="root@j", target="root@t",
                                               remote_dir="/d", rate_kbps=20480,
                                               timeout=60)
        tar = next(c for c in calls if "tar" in c)
        self.assertEqual(tar[:4], ["nice", "-n", "19", "ionice"])
        self.assertIn("-cf", tar)
        for compressed in ("-z", "-j", "--zstd", "--gzip"):
            self.assertNotIn(compressed, tar, "QA is already zstd-compressed")
        self.assertEqual(result["manifest_sha256"], "abc")

    def test_remote_failure_raises_rather_than_reporting_success(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root) / "s"
            staging.mkdir()
            handle = mock.MagicMock()
            handle.stdout = mock.MagicMock()
            handle.stderr = mock.MagicMock()
            handle.stderr.read.return_value = b""
            handle.returncode = 0
            with mock.patch.object(remote.subprocess, "Popen", return_value=handle), \
                 mock.patch.object(remote, "_has_pv", return_value=False), \
                 mock.patch.object(remote.subprocess, "run",
                                   return_value=mock.Mock(returncode=1, stdout="",
                                                          stderr="No space left on device")):
                with self.assertRaises(remote.RemoteError) as caught:
                    remote.push_directory(staging, key=pathlib.Path("/k"), jump="j",
                                          target="t", remote_dir="/d", rate_kbps=0,
                                          timeout=60)
        self.assertIn("No space left", str(caught.exception))

    def test_non_json_output_is_a_failure(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            staging = pathlib.Path(root) / "s"
            staging.mkdir()
            handle = mock.MagicMock()
            handle.stdout = mock.MagicMock()
            handle.stderr = mock.MagicMock()
            handle.stderr.read.return_value = b""
            handle.returncode = 0
            with mock.patch.object(remote.subprocess, "Popen", return_value=handle), \
                 mock.patch.object(remote, "_has_pv", return_value=False), \
                 mock.patch.object(remote.subprocess, "run",
                                   return_value=mock.Mock(returncode=0, stdout="Welcome!\n",
                                                          stderr="")):
                with self.assertRaises(remote.RemoteError):
                    remote.push_directory(staging, key=pathlib.Path("/k"), jump="j",
                                          target="t", remote_dir="/d", rate_kbps=0,
                                          timeout=60)


class RemoteCapacityTest(unittest.TestCase):
    """Capacity must be judged on node1, not on the production disk."""

    def test_node1_space_is_accepted(self) -> None:
        result = archive.check_remote_write(
            required_bytes=143_766_465, total_bytes=NODE1_TOTAL,
            free_bytes=NODE1_FREE, dataset_used_bytes=0,
            dataset_cap_bytes=archive.QA_CAP_BYTES)
        self.assertFalse(result["warn"])

    def test_dataset_cap_blocks_before_the_disk_fills(self) -> None:
        with self.assertRaises(archive.StoragePolicyError) as caught:
            archive.check_remote_write(
                required_bytes=10 * 1024**3, total_bytes=NODE1_TOTAL,
                free_bytes=NODE1_FREE,
                dataset_used_bytes=archive.QA_CAP_BYTES - 1024,
                dataset_cap_bytes=archive.QA_CAP_BYTES)
        self.assertIn("dataset cap exceeded", str(caught.exception))

    def test_block_and_emergency_thresholds_hold(self) -> None:
        total = 1000
        # required=1, so projected_used = (total - (free - 1)) / total.
        # free=150 -> 85.1% (warn only); free=101 -> 90.0% (blocked).
        for free, expected in ((150, None), (101, "write blocked"), (50, "emergency")):
            with self.subTest(free=free):
                if expected is None:
                    archive.check_remote_write(required_bytes=1, total_bytes=total,
                                               free_bytes=free, dataset_used_bytes=0,
                                               dataset_cap_bytes=archive.QA_CAP_BYTES)
                    continue
                with self.assertRaises(archive.StoragePolicyError) as caught:
                    archive.check_remote_write(required_bytes=1, total_bytes=total,
                                               free_bytes=free, dataset_used_bytes=0,
                                               dataset_cap_bytes=archive.QA_CAP_BYTES)
                self.assertIn(expected, str(caught.exception))

    def test_warn_threshold_is_reported_not_raised(self) -> None:
        # 85% warns so the operator schedules capacity; it must not stop the
        # archive, or a nearly-full disk would start deleting the only copy.
        result = archive.check_remote_write(required_bytes=1, total_bytes=1000,
                                            free_bytes=140, dataset_used_bytes=0,
                                            dataset_cap_bytes=archive.QA_CAP_BYTES)
        self.assertTrue(result["warn"])

    def test_nonsense_capacity_fails_closed(self) -> None:
        for kwargs in ({"total_bytes": 0}, {"free_bytes": -1}, {"required_bytes": -5}):
            with self.subTest(kwargs=kwargs):
                base = {"required_bytes": 1, "total_bytes": NODE1_TOTAL,
                        "free_bytes": NODE1_FREE, "dataset_used_bytes": 0,
                        "dataset_cap_bytes": archive.QA_CAP_BYTES}
                with self.assertRaises(archive.StoragePolicyError):
                    archive.check_remote_write(**(base | kwargs))


class HourSourcesTest(unittest.TestCase):
    """A sealed hour is sharded, not flat -- 256 hex dirs with blobs below."""

    def test_shard_subdirectories_are_walked_not_refused(self) -> None:
        # Live structure, measured on cn-prod: qa_blobs/YYYY/MM/DD/HH/<2 hex>/
        # holds the blobs. The original check treated any non-file entry as a
        # hazard, so every real hour failed with "unsafe QA source entry".
        with tempfile.TemporaryDirectory() as root:
            source = pathlib.Path(root)
            hour_dir = source / "qa_blobs" / HOUR.strftime(archive.HOUR)
            for shard in ("00", "9f", "ff"):
                (hour_dir / shard).mkdir(parents=True)
                (hour_dir / shard / f"{shard}-blob.json.zst").write_bytes(b"x" * 5)
            found = archive._hour_sources(source, HOUR)
        self.assertEqual(len(found), 3)
        relatives = sorted(rel for rel, _ in found)
        self.assertTrue(relatives[0].startswith("qa_blobs/2026/11/02/01/00/"))
        self.assertIn("9f/9f-blob.json.zst", relatives[1])

    def test_symlinks_are_still_refused(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            source = pathlib.Path(root)
            hour_dir = source / "qa_blobs" / HOUR.strftime(archive.HOUR)
            (hour_dir / "00").mkdir(parents=True)
            (hour_dir / "00" / "real.json.zst").write_bytes(b"x")
            (hour_dir / "00" / "link.json.zst").symlink_to(hour_dir / "00" / "real.json.zst")
            with self.assertRaises(ValueError) as caught:
                archive._hour_sources(source, HOUR)
        self.assertIn("symlink", str(caught.exception))

    def test_an_empty_hour_is_refused(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            source = pathlib.Path(root)
            (source / "qa_blobs" / HOUR.strftime(archive.HOUR)).mkdir(parents=True)
            with self.assertRaises(ValueError) as caught:
                archive._hour_sources(source, HOUR)
        self.assertIn("no QA files", str(caught.exception))


class PushHourTest(unittest.TestCase):
    def _arena(self, stack):
        root = pathlib.Path(stack.enter_context(tempfile.TemporaryDirectory()))
        source = root / "app"
        # Mirror the live sharded layout rather than a flat hour.
        hour_dir = source / "qa_blobs" / HOUR.strftime(archive.HOUR)
        (hour_dir / "0a").mkdir(parents=True)
        (hour_dir / "f3").mkdir(parents=True)
        (hour_dir / "0a" / "a.json.zst").write_bytes(b"alpha")
        (hour_dir / "f3" / "b.json.zst").write_bytes(b"beta")
        receipts = root / "qa-node1"
        receipts.mkdir()
        return source, receipts

    def _remote(self):
        return {"key": pathlib.Path(__file__), "jump": "root@j", "target": "root@t",
                "remote_root": "/storage/tokenkey-archive", "rate_kbps": 0,
                "timeout": 60}

    def test_receipt_written_only_after_the_digest_matches(self) -> None:
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            captured = {}

            def fake_push(staging, **kwargs):
                manifest = json.loads((staging / "manifest.json").read_text())
                captured["manifest"] = manifest
                captured["remote_dir"] = kwargs["remote_dir"]
                digest, _ = archive.sha256_file(staging / "manifest.json")
                return {"ok": True, "manifest_sha256": digest,
                        "file_count": manifest["file_count"],
                        "total_bytes": manifest["total_bytes"], "already_present": False}

            stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                  side_effect=fake_push))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": NODE1_TOTAL, "free_bytes": NODE1_FREE,
                              "qa_used_bytes": 0}))
            result = archive.push_hour(source, receipts, "prod", HOUR, self._remote())
            written = json.loads(archive.receipt_path(receipts, "prod", HOUR).read_text())

        self.assertTrue(result["ok"])
        self.assertEqual(result["transport"], "ssh-tar")
        self.assertEqual(result["file_count"], 2)
        self.assertEqual(result["total_bytes"], 9)
        self.assertEqual(captured["remote_dir"],
                         "/storage/tokenkey-archive/qa-raw/prod/2026/11/02/01")
        self.assertEqual(written["manifest_sha256"], result["manifest_sha256"])
        # Every file carries its own digest, so a later verify can detect rot.
        self.assertTrue(all("sha256" in f for f in captured["manifest"]["files"]))

    def test_digest_mismatch_leaves_no_receipt(self) -> None:
        # The hour must stay deletable-blocked: cleanup keys off the receipt, so
        # writing one for an unverified push would authorise deleting the only
        # remaining copy.
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "push_directory",
                return_value={"ok": True, "manifest_sha256": "0" * 64,
                              "file_count": 2, "total_bytes": 9}))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": NODE1_TOTAL, "free_bytes": NODE1_FREE,
                              "qa_used_bytes": 0}))
            with self.assertRaises(ValueError) as caught:
                archive.push_hour(source, receipts, "prod", HOUR, self._remote())
        self.assertIn("digest mismatch", str(caught.exception))
        self.assertFalse(archive.receipt_path(receipts, "prod", HOUR).exists())

    def test_byte_or_file_count_mismatch_leaves_no_receipt(self) -> None:
        import contextlib
        for bad in ({"file_count": 1}, {"total_bytes": 4}):
            with self.subTest(bad=bad), contextlib.ExitStack() as stack:
                source, receipts = self._arena(stack)

                def fake_push(staging, **kwargs):
                    manifest = json.loads((staging / "manifest.json").read_text())
                    digest, _ = archive.sha256_file(staging / "manifest.json")
                    return {"ok": True, "manifest_sha256": digest,
                            "file_count": manifest["file_count"],
                            "total_bytes": manifest["total_bytes"]} | bad

                stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                      side_effect=fake_push))
                stack.enter_context(mock.patch.object(
                    archive.node1_remote, "remote_capacity",
                    return_value={"total_bytes": NODE1_TOTAL, "free_bytes": NODE1_FREE,
                                  "qa_used_bytes": 0}))
                with self.assertRaises(ValueError):
                    archive.push_hour(source, receipts, "prod", HOUR, self._remote())
                self.assertFalse(archive.receipt_path(receipts, "prod", HOUR).exists())

    def test_staging_is_removed_even_when_the_push_fails(self) -> None:
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "push_directory",
                side_effect=remote.RemoteError("link down")))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": NODE1_TOTAL, "free_bytes": NODE1_FREE,
                              "qa_used_bytes": 0}))
            with self.assertRaises(remote.RemoteError):
                archive.push_hour(source, receipts, "prod", HOUR, self._remote())
            leftovers = [p for p in receipts.iterdir() if p.name.startswith(".push-")]
        self.assertEqual(leftovers, [], "a failed push must not leave staging behind")

    def test_source_is_never_modified(self) -> None:
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            before = {p: p.read_bytes() for p in source.rglob("*") if p.is_file()}
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "push_directory",
                side_effect=remote.RemoteError("boom")))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": NODE1_TOTAL, "free_bytes": NODE1_FREE,
                              "qa_used_bytes": 0}))
            with self.assertRaises(remote.RemoteError):
                archive.push_hour(source, receipts, "prod", HOUR, self._remote())
            after = {p: p.read_bytes() for p in source.rglob("*") if p.is_file()}
        self.assertEqual(before, after)

    def test_capacity_is_checked_before_any_bytes_move(self) -> None:
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            pushed = []
            stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                  side_effect=lambda *a, **k: pushed.append(1)))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": 1000, "free_bytes": 20, "qa_used_bytes": 0}))
            with self.assertRaises(ValueError) as caught:
                archive.push_hour(source, receipts, "prod", HOUR, self._remote())
        self.assertIn("emergency", str(caught.exception))
        self.assertEqual(pushed, [], "no stream may start once capacity is refused")

    def test_an_existing_valid_receipt_short_circuits(self) -> None:
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            archive.atomic_json(archive.receipt_path(receipts, "prod", HOUR),
                                {"schema_version": archive.SCHEMA, "ok": True,
                                 "file_count": 2, "total_bytes": 9})
            pushed = []
            stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                  side_effect=lambda *a, **k: pushed.append(1)))
            result = archive.push_hour(source, receipts, "prod", HOUR, self._remote())
        self.assertTrue(result["already_completed"])
        self.assertEqual(pushed, [], "an archived hour must not be re-pushed")


if __name__ == "__main__":
    unittest.main()
