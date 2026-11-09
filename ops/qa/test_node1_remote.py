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
from pathlib import Path
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

    def test_a_receipt_matching_node1_short_circuits(self) -> None:
        import contextlib
        digest = "b" * 64
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            archive.atomic_json(archive.receipt_path(receipts, "prod", HOUR),
                                {"schema_version": archive.SCHEMA, "ok": True,
                                 "file_count": 2, "total_bytes": 9,
                                 "manifest_sha256": digest})
            pushed = []
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_manifest_digest", return_value=digest))
            stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                  side_effect=lambda *a, **k: pushed.append(1)))
            result = archive.push_hour(source, receipts, "prod", HOUR, self._remote())
        self.assertTrue(result["already_completed"])
        self.assertEqual(pushed, [], "an hour node1 really holds must not be re-pushed")

    def test_a_receipt_without_node1_data_is_repushed(self) -> None:
        # The live failure: prod 07:00 and 08:00 carried receipts written by an
        # earlier local-copy run while node1 held nothing, so cleanup blocked
        # them forever and this path skipped them as done.
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            archive.atomic_json(archive.receipt_path(receipts, "prod", HOUR),
                                {"schema_version": archive.SCHEMA, "ok": True,
                                 "file_count": 2, "total_bytes": 9,
                                 "manifest_sha256": "c" * 64})
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_manifest_digest", return_value=""))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": 10 << 40, "free_bytes": 9 << 40,
                              "qa_used_bytes": 0}))
            pushed = []

            def fake_push(staging, **kwargs):
                pushed.append(staging)
                manifest = json.loads((staging / "manifest.json").read_text())
                return {"manifest_sha256": archive.sha256_file(staging / "manifest.json")[0],
                        "file_count": manifest["file_count"],
                        "total_bytes": sum(e["bytes"] for e in manifest["files"]),
                        "already_present": False}

            stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                  side_effect=fake_push))
            result = archive.push_hour(source, receipts, "prod", HOUR, self._remote())
        self.assertEqual(len(pushed), 1, "an hour node1 lacks must be re-pushed")
        self.assertNotIn("already_completed", result)
        self.assertEqual(result["repushed_because"],
                         "node1 holds no manifest for this hour")

    def test_a_digest_mismatch_is_repushed(self) -> None:
        import contextlib
        with contextlib.ExitStack() as stack:
            source, receipts = self._arena(stack)
            archive.atomic_json(archive.receipt_path(receipts, "prod", HOUR),
                                {"schema_version": archive.SCHEMA, "ok": True,
                                 "file_count": 2, "total_bytes": 9,
                                 "manifest_sha256": "c" * 64})
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_manifest_digest", return_value="d" * 64))
            stack.enter_context(mock.patch.object(
                archive.node1_remote, "remote_capacity",
                return_value={"total_bytes": 10 << 40, "free_bytes": 9 << 40,
                              "qa_used_bytes": 0}))
            pushed = []

            def fake_push(staging, **kwargs):
                pushed.append(staging)
                manifest = json.loads((staging / "manifest.json").read_text())
                return {"manifest_sha256": archive.sha256_file(staging / "manifest.json")[0],
                        "file_count": manifest["file_count"],
                        "total_bytes": sum(e["bytes"] for e in manifest["files"]),
                        "already_present": False}

            stack.enter_context(mock.patch.object(archive.node1_remote, "push_directory",
                                                  side_effect=fake_push))
            result = archive.push_hour(source, receipts, "prod", HOUR, self._remote())
        self.assertEqual(len(pushed), 1)
        self.assertEqual(result["repushed_because"],
                         "node1 manifest digest does not match the local receipt")


if __name__ == "__main__":
    unittest.main()


class ArchiveTimerWiringTest(unittest.TestCase):
    """The timer must actually push off-host, or enabling it is a liability."""

    def setUp(self) -> None:
        self.host = _load("local_maintenance_host")
        self.remote = {"target": "root@10.10.2.11", "jump": "root@180.76.181.161",
                       "key": "/etc/tokenkey/node1-archive.key",
                       "root": "/storage/tokenkey-archive"}

    def _exec_start(self, units: dict) -> str:
        service = units["tokenkey-qa-archive.service"]
        return next(l for l in service.splitlines() if l.startswith("ExecStart="))

    def test_remote_settings_reach_the_exec_line(self) -> None:
        exec_start = self._exec_start(
            self.host.unit_files("/b", "archive", "cn-prod", self.remote))
        self.assertIn("--remote-target root@10.10.2.11", exec_start)
        self.assertIn("--remote-jump root@180.76.181.161", exec_start)
        self.assertIn("--remote-key /etc/tokenkey/node1-archive.key", exec_start)
        self.assertIn("--remote-root /storage/tokenkey-archive", exec_start)
        self.assertIn("--environment cn-prod", exec_start)

    def test_without_remote_the_archive_stays_local(self) -> None:
        # This is the state that must never be enabled in production: the
        # payload would land on /var/lib/tokenkey, the live database's own
        # volume, which is neither off-host nor somewhere QA can grow.
        exec_start = self._exec_start(self.host.unit_files("/b", "archive", "prod"))
        self.assertNotIn("--remote-target", exec_start)
        self.assertIn("--node1-root /var/lib/tokenkey/qa-node1", exec_start)

    def test_receipt_root_stays_local_in_remote_mode(self) -> None:
        # The cleanup gate reads receipts from disk. Pointing --node1-root at
        # the remote path would make log retention depend on the network.
        exec_start = self._exec_start(
            self.host.unit_files("/b", "archive", "prod", self.remote))
        self.assertIn("--node1-root /var/lib/tokenkey/qa-node1", exec_start)

    def test_timer_runs_every_four_hours_off_peak(self) -> None:
        units = self.host.unit_files("/b", "archive", "prod", self.remote)
        calendar = next(l for l in units["tokenkey-qa-archive.timer"].splitlines()
                        if l.startswith("OnCalendar="))
        self.assertEqual(calendar, "OnCalendar=*-*-* 01/4:00:00 UTC")

    def test_archive_service_yields_to_the_live_workload(self) -> None:
        service = self.host.unit_files("/b", "archive", "prod", self.remote)["tokenkey-qa-archive.service"]
        self.assertIn("Nice=19", service)
        self.assertIn("IOSchedulingClass=idle", service)
        self.assertIn("UMask=0077", service)

    def test_unknown_environment_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            self.host.unit_files("/b", "archive", "staging", self.remote)

    def test_install_units_actually_runs_with_remote(self) -> None:
        # Source-level assertions missed a NameError: `remote` was built in
        # install() but referenced inside install_units(), which has its own
        # scope. The installer failed on the live host at the moment it tried
        # to write units. Execute the path instead of reading it.
        host = _load("local_maintenance_host")
        import inspect
        signature = inspect.signature(host.install_units)
        self.assertIn("remote", signature.parameters,
                      "install_units must accept remote, not close over it")
        captured = {}

        def fake_unit_files(bundle, phase, environment="prod", remote=None):
            captured[phase] = remote
            return {f"tokenkey-qa-{phase}.service": "x", f"tokenkey-qa-{phase}.timer": "y"}

        with mock.patch.object(host, "unit_files", side_effect=fake_unit_files), \
             mock.patch.object(host.Path, "mkdir", lambda *a, **k: None), \
             self.assertRaises(Exception):
            # Writing real units needs root; the call must at least get past
            # the dict comprehension that previously raised NameError.
            host.install_units("/b", ["archive"], "cn-prod", self.remote)
        self.assertEqual(captured.get("archive"), self.remote,
                         "remote must reach unit_files through install_units")

    def test_install_passes_remote_through_to_unit_files(self) -> None:
        # Asserting on unit_files alone leaves a gap: install() could simply
        # stop forwarding `remote` and every other test here would still pass,
        # while the deployed timer silently wrote to the local disk. Pin the
        # call site in the source.
        import re
        source = (HERE / "local_maintenance_host.py").read_text()
        # Match the call site, not the `def` line -- the signature carries
        # `remote=None` and would satisfy a naive substring check forever.
        calls = [l for l in source.splitlines()
                 if "unit_files(bundle, phase" in l and not l.lstrip().startswith("def ")]
        self.assertEqual(len(calls), 1, f"expected one call site, found {calls}")
        self.assertRegex(
            calls[0], r"unit_files\(bundle,\s*phase,\s*environment,\s*remote\)",
            "install() must forward the remote settings to unit_files")

    def test_install_records_the_remote_in_config(self) -> None:
        # config.json is what an operator reads on the host to see whether the
        # timer pushes off-host or writes locally.
        source = (HERE / "local_maintenance_host.py").read_text()
        self.assertIn("'node1_remote': remote", source)

    def test_install_refuses_a_missing_key(self) -> None:
        source = (HERE / "local_maintenance_host.py").read_text()
        self.assertIn("node1 ssh key is missing", source,
                      "installing a push timer without its key would fail every run")


class ArchiveEnvironmentTest(unittest.TestCase):
    """Which node1 namespace a host writes to must never be a silent default."""

    def setUp(self) -> None:
        self.host = _load("local_maintenance_host")

    def _args(self, **kwargs):
        defaults = {"node1_environment": "", "include_node1_archive": False}
        return type("Args", (), defaults | kwargs)()

    def test_enabling_without_an_environment_is_refused(self) -> None:
        # cn-prod's manifest has no `environment` key. Defaulting to 'prod'
        # would push its QA into prod's node1 directory, mixing two hosts.
        with self.assertRaises(ValueError) as caught:
            self.host.resolve_environment({}, self._args(include_node1_archive=True))
        self.assertIn("--node1-environment", str(caught.exception))

    def test_an_explicit_flag_is_used(self) -> None:
        chosen = self.host.resolve_environment(
            {}, self._args(include_node1_archive=True, node1_environment="cn-prod"))
        self.assertEqual(chosen, "cn-prod")

    def test_a_flag_contradicting_the_manifest_is_refused(self) -> None:
        with self.assertRaises(ValueError) as caught:
            self.host.resolve_environment(
                {"environment": "prod"},
                self._args(include_node1_archive=True, node1_environment="cn-prod"))
        self.assertIn("manifest declares environment", str(caught.exception))

    def test_a_declared_manifest_environment_is_honoured(self) -> None:
        chosen = self.host.resolve_environment(
            {"environment": "cn-prod"}, self._args(include_node1_archive=True))
        self.assertEqual(chosen, "cn-prod")

    def test_unknown_values_are_refused(self) -> None:
        with self.assertRaises(ValueError):
            self.host.resolve_environment({"environment": "staging"},
                                          self._args(include_node1_archive=True))

    def test_without_the_archive_the_old_default_still_applies(self) -> None:
        # Hosts that never enable the archive must keep installing as before.
        self.assertEqual(self.host.resolve_environment({}, self._args()), "prod")

    def test_the_guard_runs_before_plan_mode_returns(self) -> None:
        # Resolved after `if not args.apply: return`, a bad environment would
        # print a plan that could never be applied -- which is exactly what
        # the first live plan run did.
        source = (HERE / "local_maintenance_host.py").read_text()
        body = source[source.index("def install(args):"):]
        self.assertLess(body.index("resolve_environment(manifest, args)"),
                        body.index("if not args.apply:"),
                        "the environment must be resolved before the plan return")


class BundleCompletenessTest(unittest.TestCase):
    """A bundle missing a module fails at the first timer run, not at install."""

    def setUp(self) -> None:
        self.host = _load("local_maintenance_host")
        self.source = (HERE / "local_maintenance_host.py").read_text()

    def test_every_module_archive_imports_is_bundled(self) -> None:
        # node1_archive imports node1_remote at module scope. The first live
        # install shipped without it and the service died with
        # ModuleNotFoundError: No module named 'node1_remote'.
        import ast
        archive_source = (HERE / "node1_archive.py").read_text()
        imported = set()
        for node in ast.walk(ast.parse(archive_source)):
            if isinstance(node, ast.Import):
                imported.update(alias.name for alias in node.names)
            elif isinstance(node, ast.ImportFrom) and node.module:
                imported.add(node.module)
        local_imports = {name for name in imported if (HERE / f"{name}.py").is_file()}
        self.assertIn("node1_remote", local_imports,
                      "fixture assumption: archive imports the transport")
        for name in local_imports:
            with self.subTest(module=name):
                self.assertIn(f"staged/'{name}.py'", self.source,
                              f"{name}.py must be copied into the bundle")

    def test_transport_is_part_of_the_bundle_digest(self) -> None:
        # Otherwise editing the transport alone would reuse a stale bundle.
        self.assertIn("ARCHIVE_TRANSPORT.read_bytes()", self.source)
        material = self.source[self.source.index("bundle_material = "):
                               self.source.index("bundle_digest = ")]
        self.assertIn("ARCHIVE_TRANSPORT", material,
                      "the transport must contribute to the bundle digest")

    def test_a_stale_bundle_missing_the_transport_is_rebuilt(self) -> None:
        # Otherwise a bundle built before the transport existed is reused, and
        # the timer keeps failing with ModuleNotFoundError. Parse the
        # assignments rather than slicing source text, which matched the string
        # elsewhere in the file and passed even with the check deleted.
        import ast
        assignments = [
            node for node in ast.walk(ast.parse(self.source))
            if isinstance(node, ast.Assign)
            and any(isinstance(t, ast.Name) and t.id == "bundle_mismatch"
                    for t in node.targets)
        ]
        self.assertTrue(assignments, "expected bundle_mismatch assignments")
        checked = " ".join(ast.unparse(node.value) for node in assignments)
        self.assertIn("node1_remote.py", checked,
                      "an existing bundle without the transport must count as a mismatch")


class BackwardCompatibilityTest(unittest.TestCase):
    """Callers that predate the remote options must keep working."""

    def test_install_tolerates_an_args_without_the_new_options(self) -> None:
        # The existing suite builds a minimal Args stub; reading the new
        # options as attributes broke test_install_archive_requires_explicit
        # _opt_in with AttributeError in CI, which my own tests never ran.
        import ast
        source = (HERE / "local_maintenance_host.py").read_text()
        install = next(node for node in ast.walk(ast.parse(source))
                       if isinstance(node, ast.FunctionDef) and node.name == "install")
        body = ast.unparse(install)
        for option in ("node1_remote_target", "node1_remote_jump",
                       "node1_remote_key", "node1_remote_root"):
            with self.subTest(option=option):
                self.assertNotIn(f"args.{option}", body,
                                 f"{option} must be read via getattr with a default")

    def test_defaults_are_named_once(self) -> None:
        # argparse and install() both need them; two literals would drift.
        host = _load("local_maintenance_host")
        self.assertEqual(host.DEFAULT_REMOTE_JUMP, "root@180.76.181.161")
        self.assertEqual(str(host.DEFAULT_REMOTE_KEY), "/etc/tokenkey/node1-archive.key")
        self.assertEqual(host.DEFAULT_REMOTE_ROOT, "/storage/tokenkey-archive")


class ReceiptIsNotEvidenceTest(unittest.TestCase):
    """A local receipt claims an archive; node1 holding it is the proof.

    On 2026-11-02 an earlier local-copy run wrote valid receipts for prod
    07:00 and 08:00 with correct file counts and manifest digests, but the
    payload never reached node1. The cleanup gate then blocked those hours
    forever while this path skipped them as already done -- 12,468 files stuck
    with one copy and a recurring page. 31 of 44 prod receipts share that
    provenance.
    """

    def setUp(self) -> None:
        self.archive = _load("node1_archive")
        self.source = (HERE / "node1_archive.py").read_text()

    def test_remote_path_verifies_node1_before_skipping(self) -> None:
        push = self.source[self.source.index("def push_hour"):]
        push = push[:push.index("\ndef ", 1)]
        self.assertIn("remote_manifest_digest", push,
                      "push_hour must read the digest back from node1 before "
                      "trusting a local receipt")
        skip = push[push.index("if receipt.exists():"):push.index("required_bytes")]
        self.assertIn("remote_digest and remote_digest == existing.get", skip,
                      "the skip must require a digest match, not just a receipt")

    def test_a_mismatched_digest_triggers_a_repush(self) -> None:
        push = self.source[self.source.index("def push_hour"):]
        push = push[:push.index("\ndef ", 1)]
        self.assertIn("node1 holds no manifest for this hour", push)
        self.assertIn("node1 manifest digest does not match the local receipt", push)
        self.assertIn("repushed_because", push,
                      "the published receipt must record why it was re-pushed")

    def test_an_unparseable_receipt_still_fails_loudly(self) -> None:
        push = self.source[self.source.index("def push_hour"):]
        push = push[:push.index("\ndef ", 1)]
        self.assertIn("existing node1 receipt is invalid", push)


class RemoteManifestDigestTest(unittest.TestCase):
    def setUp(self) -> None:
        self.remote = _load("node1_remote")

    def _run(self, stdout: str, returncode: int = 0):
        completed = type("R", (), {"stdout": stdout, "stderr": "", "returncode": returncode})()
        return mock.patch.object(self.remote.subprocess, "run", return_value=completed)

    def test_absent_manifest_reports_empty(self) -> None:
        with self._run("ABSENT\n"):
            digest = self.remote.remote_manifest_digest(
                Path("/k"), "j", "t", "/storage/tokenkey-archive/qa-raw/prod/2026/11/02/07")
        self.assertEqual(digest, "")

    def test_a_present_manifest_returns_its_digest(self) -> None:
        expected = "a" * 64
        with self._run(expected + "\n"):
            self.assertEqual(
                self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d"), expected)

    def test_garbage_output_is_refused(self) -> None:
        # Treating unparseable output as "absent" would trigger an endless
        # re-push; treating it as a match would skip a missing hour.
        for bad in ("not-a-digest", "a" * 63, "z" * 64, ""):
            with self.subTest(out=bad), self._run(bad + "\n"):
                with self.assertRaises(self.remote.RemoteError):
                    self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d")

    def test_a_failed_probe_raises(self) -> None:
        with self._run("", returncode=255):
            with self.assertRaises(self.remote.RemoteError):
                self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d")

    def test_the_probe_only_reads(self) -> None:
        source = (HERE / "node1_remote.py").read_text()
        probe = source[source.index("def remote_manifest_digest"):]
        probe = probe[:probe.index("\ndef ", 1)]
        script = probe[probe.index("script = ("):probe.index("command = ")]
        for mutating in ("rm ", "mv ", "tar -x", "mkdir", "touch", ">>", "> "):
            with self.subTest(op=mutating):
                self.assertNotIn(mutating, script)


class ReceiptReadabilityTest(unittest.TestCase):
    """The gate runs as uid 1000 in a container; a 0600 root receipt is invisible.

    On prod the gate reported "permission denied" for every hour it checked --
    all 44 receipts were mode 600 owned by root, written by the root archive
    process, while the cleanup container runs --user 1000:1000 with the tree
    mounted read-only. The gate had never actually read a receipt; it was
    blocking on the manifest check alone.
    """

    def setUp(self) -> None:
        self.archive = _load("node1_archive")

    def test_a_new_receipt_is_group_and_other_readable(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            target = pathlib.Path(root) / "prod" / "2026" / "11" / "02" / "07.json"
            self.archive.atomic_json(target, {"ok": True})
            mode = target.stat().st_mode & 0o777
            self.assertTrue(mode & 0o044, f"receipt mode {oct(mode)} is unreadable to the gate")

    def test_receipt_directories_are_traversable(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            target = pathlib.Path(root) / "prod" / "2026" / "11" / "02" / "07.json"
            self.archive.atomic_json(target, {"ok": True})
            for parent in (target.parent, target.parent.parent):
                mode = parent.stat().st_mode & 0o777
                self.assertTrue(mode & 0o055,
                                f"{parent} mode {oct(mode)} blocks traversal")

    def test_existing_unreadable_receipts_are_repaired(self) -> None:
        # The 44 receipts already on disk were written before this fix.
        with tempfile.TemporaryDirectory() as root:
            receipts = pathlib.Path(root) / "receipts"
            old = receipts / "prod" / "2026" / "11" / "02" / "07.json"
            old.parent.mkdir(parents=True)
            old.write_text('{"ok": true}')
            old.chmod(0o600)
            old.parent.chmod(0o700)
            self.archive.ensure_readable_tree(receipts)
            self.assertTrue(old.stat().st_mode & 0o044,
                            "an existing receipt must be made readable")
            self.assertTrue(old.parent.stat().st_mode & 0o055,
                            "an existing directory must be made traversable")

    def test_the_repair_runs_at_the_end_of_every_archive_run(self) -> None:
        # Calling ensure_readable_tree in a test proves the function works, not
        # that anything invokes it. Without this, deleting the call from main()
        # left the suite green while the 44 existing receipts stayed invisible.
        import ast
        source = (HERE / "node1_archive.py").read_text()
        # main() only parses arguments; run() holds the archive flow.
        run = next(node for node in ast.walk(ast.parse(source))
                   if isinstance(node, ast.FunctionDef) and node.name == "run")
        body = ast.unparse(run)
        self.assertIn("ensure_readable_tree", body,
                      "run() must repair receipt permissions after each archive pass")
        self.assertLess(body.index("atomic_json(node1"), body.index("ensure_readable_tree"),
                        "repair must follow the last receipt write")

    def test_a_0700_node1_root_is_made_traversable(self) -> None:
        # 2026-11-08 prod rebuild: run() recreated node1_root as 0700 and the
        # cleanup container (uid 1000, bind-mounts node1_root) could not reach
        # any receipt below it, so 24 hours stayed blocked.
        with tempfile.TemporaryDirectory() as root:
            node1 = pathlib.Path(root) / "qa-node1"
            node1.mkdir(mode=0o700)
            self.archive.ensure_traversable(node1)
            self.assertEqual(node1.stat().st_mode & 0o055, 0o055,
                             "node1_root must be traversable by the cleanup container")

    def test_run_repairs_the_node1_root_before_the_receipt_tree(self) -> None:
        import ast
        source = (HERE / "node1_archive.py").read_text()
        run = next(node for node in ast.walk(ast.parse(source))
                   if isinstance(node, ast.FunctionDef) and node.name == "run")
        body = ast.unparse(run)
        self.assertIn("ensure_traversable(node1)", body,
                      "run() must keep node1_root traversable after each archive pass")
        self.assertLess(body.index("atomic_json(node1"), body.index("ensure_traversable(node1)"))

    def test_repair_leaves_non_json_alone(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            receipts = pathlib.Path(root) / "receipts"
            receipts.mkdir()
            secret = receipts / "key.pem"
            secret.write_text("x")
            secret.chmod(0o600)
            self.archive.ensure_readable_tree(receipts)
            self.assertEqual(secret.stat().st_mode & 0o777, 0o600,
                             "only receipts are widened, never other files")


class ManifestProbeRetryTest(unittest.TestCase):
    """A jump-host reset must not turn a good hour into a failed one.

    cn-prod's archive reported 23 completed / 1 failed on 2026-11-03 solely
    because one manifest probe hit "kex_exchange_identification: read:
    Connection reset by peer". That also made the whole unit exit 2, so the
    newly-added qa-archive monitor immediately flagged it as failed.
    """

    def setUp(self) -> None:
        self.remote = _load("node1_remote")

    def _results(self, *sequence):
        made = []
        for code, out in sequence:
            made.append(type("R", (), {"stdout": out, "stderr": "", "returncode": code})())
        return made

    def test_a_transient_reset_is_retried_and_succeeds(self) -> None:
        digest = "a" * 64
        attempts = self._results((255, ""), (0, digest + "\n"))
        with mock.patch.object(self.remote.subprocess, "run", side_effect=attempts), \
             mock.patch.object(self.remote.time, "sleep"):
            self.assertEqual(
                self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d"), digest)

    def test_a_persistent_failure_still_raises_after_retries(self) -> None:
        attempts = self._results((255, ""), (255, ""), (255, ""))
        with mock.patch.object(self.remote.subprocess, "run", side_effect=attempts), \
             mock.patch.object(self.remote.time, "sleep"):
            with self.assertRaises(self.remote.RemoteError) as caught:
                self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d")
        self.assertIn("after 3 attempts", str(caught.exception))

    def test_a_first_attempt_success_makes_no_further_calls(self) -> None:
        digest = "b" * 64
        calls = []

        def once(*args, **kwargs):
            calls.append(1)
            return type("R", (), {"stdout": digest + "\n", "stderr": "", "returncode": 0})()

        with mock.patch.object(self.remote.subprocess, "run", side_effect=once), \
             mock.patch.object(self.remote.time, "sleep"):
            self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d")
        self.assertEqual(len(calls), 1, "a healthy probe must not retry")

    def test_retries_back_off_rather_than_hammering(self) -> None:
        slept = []
        attempts = self._results((255, ""), (255, ""), (0, "c" * 64 + "\n"))
        with mock.patch.object(self.remote.subprocess, "run", side_effect=attempts), \
             mock.patch.object(self.remote.time, "sleep", side_effect=slept.append):
            self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d")
        self.assertEqual(slept, [2, 4], "backoff must grow between attempts")

    def test_an_absent_manifest_is_not_retried_as_a_failure(self) -> None:
        # ABSENT is a successful answer meaning "node1 does not have it", not
        # a transport error; retrying it would triple every re-push decision.
        calls = []

        def absent(*args, **kwargs):
            calls.append(1)
            return type("R", (), {"stdout": "ABSENT\n", "stderr": "", "returncode": 0})()

        with mock.patch.object(self.remote.subprocess, "run", side_effect=absent), \
             mock.patch.object(self.remote.time, "sleep"):
            self.assertEqual(
                self.remote.remote_manifest_digest(Path("/k"), "j", "t", "/d"), "")
        self.assertEqual(len(calls), 1)
