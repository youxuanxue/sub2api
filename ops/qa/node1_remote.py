#!/usr/bin/env python3
"""Push one sealed QA hour to node1 over SSH as a single tar stream.

Why a stream rather than per-file copies: QA raw is a many-small-files dataset,
not a bulk-bytes one. Measured 2026-11-03 -- prod holds 207,048 files averaging
37.5 KiB (91% under 64 KiB), cn-prod 107,118 averaging 22.8 KiB. Copying an
hour file-by-file over a mounted remote directory pays several network
round-trips per file; one hour of prod data (3,430 files) measured 28.5 seconds
as a tar stream versus an estimated ~34 minutes file-by-file. The files are
already zstd-compressed, so the stream is deliberately NOT compressed again
(measured 0% gain).

What this module does NOT change: the manifest with a per-file sha256, the
staging directory published by atomic rename, the capacity policy, and the
receipt. Those live in node1_archive.py and remain the source of truth for
whether an hour is safely archived.

The receipt is written locally AFTER the remote side confirms it recomputed the
same manifest digest. Local receipts matter because the production cleanup gate
reads them to decide whether an hour may be deleted; making that read depend on
a live network path would couple log retention to node1 availability.
"""
from __future__ import annotations

import json
import shlex
import subprocess
import sys
import time
from pathlib import Path

# Both production hosts reach node1 through a jump host on the WireGuard
# network; the cloud security group only allows 22/443/8080, so port 22 through
# the jump host is the stable path. Primary and fallback are both verified.
DEFAULT_JUMP = "root@180.76.181.161"
FALLBACK_JUMP = "root@180.76.98.95"
DEFAULT_TARGET = "root@10.10.2.11"
FALLBACK_TARGET = "root@10.10.1.11"
DEFAULT_KEY = Path("/etc/tokenkey/node1-archive.key")
DEFAULT_REMOTE_ROOT = "/storage/tokenkey-archive"
# nice/ionice keep the push behind the live gateway workload, as the approved
# design requires ("低优先级、带宽上限"). The rate limit is applied to the
# stream rather than to ssh so it also bounds local disk reads.
DEFAULT_RATE_KBPS = 20480  # 20 MiB/s; prod measured 4.8 MiB/s in practice
SSH_OPTIONS = (
    "-o", "BatchMode=yes",
    "-o", "StrictHostKeyChecking=accept-new",
    "-o", "ConnectTimeout=15",
    "-o", "ServerAliveInterval=30",
    "-o", "ServerAliveCountMax=3",
)


class RemoteError(RuntimeError):
    pass


def ssh_command(key: Path, jump: str, target: str) -> list[str]:
    """ssh argv that routes through the jump host with this key only.

    ``-J`` alone does not reuse ``-i`` for the jump connection, so the proxy is
    spelled out; without this the jump authenticates as the invoking user and
    fails with "Permission denied (publickey)".
    """
    proxy = " ".join([
        "ssh", "-i", shlex.quote(str(key)),
        "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
        "-o", "ConnectTimeout=15", "-W", "%h:%p", jump,
    ])
    return ["ssh", *SSH_OPTIONS, "-o", f"ProxyCommand={proxy}",
            "-i", str(key), "-o", "IdentitiesOnly=yes", target]


def _remote_script(remote_dir: str) -> str:
    """Unpack into a staging dir, then publish by rename; print a JSON summary.

    The remote side recomputes the manifest digest itself. A stream that
    arrived truncated or reordered produces a different digest, so the caller
    can refuse to write a receipt instead of trusting the exit code.
    """
    quoted = shlex.quote(remote_dir)
    return f"""
set -eu
dir={quoted}
parent=$(dirname "$dir")
mkdir -p "$parent"
staging=$(mktemp -d "$parent/.incoming-XXXXXX")
cleanup() {{ rm -rf "$staging"; }}
trap cleanup EXIT
tar -C "$staging" -xf - --no-same-owner --no-same-permissions
test -f "$staging/manifest.json" || {{ echo '{{"ok":false,"error":"manifest.json missing from stream"}}'; exit 1; }}
files=$(find "$staging" -type f ! -name manifest.json | wc -l)
bytes=$(find "$staging" -type f ! -name manifest.json -printf '%s\\n' | awk '{{t+=$1}} END {{print t+0}}')
digest=$(sha256sum "$staging/manifest.json" | cut -d' ' -f1)
if [ -e "$dir" ]; then
  existing=$(sha256sum "$dir/manifest.json" 2>/dev/null | cut -d' ' -f1 || echo missing)
  if [ "$existing" = "$digest" ]; then
    printf '{{"ok":true,"already_present":true,"manifest_sha256":"%s","file_count":%s,"total_bytes":%s}}\\n' "$digest" "$files" "$bytes"
    exit 0
  fi
  printf '{{"ok":false,"error":"target exists with a different manifest","manifest_sha256":"%s"}}\\n' "$existing"
  exit 1
fi
chmod -R go-rwx "$staging"
mv "$staging" "$dir"
trap - EXIT
printf '{{"ok":true,"already_present":false,"manifest_sha256":"%s","file_count":%s,"total_bytes":%s}}\\n' "$digest" "$files" "$bytes"
"""


def _read_probe(command: list[str], script: str, operation: str) -> subprocess.CompletedProcess:
    """Retry bounded, read-only SSH probes after a jump-host disconnect.

    Both manifest and capacity checks share this policy. A successful reply
    still goes through the caller's validation; malformed data is never a
    reason to retry or to permit a write.
    """
    attempts = 3
    for attempt in range(1, attempts + 1):
        try:
            result = subprocess.run(command, input=script, text=True,
                                    capture_output=True, check=False, timeout=120)
        except subprocess.TimeoutExpired:
            error = "timed out after 120 seconds"
        else:
            if result.returncode == 0:
                return result
            error = (result.stderr or result.stdout).strip()[-600:]
        print(json.dumps({"event": "qa_archive_probe_failed", "operation": operation,
                          "attempt": attempt, "attempts": attempts, "error": error}),
              file=sys.stderr, flush=True)
        if attempt < attempts:
            time.sleep(2 * attempt)
    raise RemoteError(f"node1 {operation} probe failed after {attempts} attempts: {error}")


def remote_capacity(key: Path, jump: str, target: str, remote_root: str) -> dict:
    """Free space and QA receipt usage ON NODE1, not on the sending host.

    The local capacity policy measures the local filesystem, which in remote
    mode is the production disk and says nothing about the archive volume.
    """
    script = f"""
set -eu
root={shlex.quote(remote_root)}
mkdir -p "$root/receipts" "$root/qa-raw"
read -r total free <<EOF2
$(df -B1 --output=size,avail "$root" | tail -1)
EOF2
used=$(find "$root/qa-raw" -type f -printf '%s\\n' 2>/dev/null | awk '{{t+=$1}} END {{print t+0}}')
printf '{{"total_bytes":%s,"free_bytes":%s,"qa_used_bytes":%s}}\\n' "$total" "$free" "$used"
"""
    result = _read_probe([*ssh_command(key, jump, target), "bash", "-s"],
                         script, "capacity")
    try:
        return json.loads(result.stdout.strip().splitlines()[-1])
    except (ValueError, IndexError) as exc:
        raise RemoteError(f"node1 capacity probe returned no JSON: {result.stdout[-300:]}") from exc


def remote_manifest_digest(key: Path, jump: str, target: str, remote_dir: str) -> str:
    """The sha256 of one published hour's manifest on node1, or "" if absent.

    A local receipt alone is not evidence that the payload reached node1. An
    earlier local-copy run wrote receipts for 2026-11-02 07:00 and 08:00 that
    passed every local check while node1 held nothing for those hours, so the
    cleanup gate blocked them forever and the archive skipped them as already
    done. Reading the digest back is what distinguishes a real archive from a
    receipt that merely claims one.
    """
    script = (
        f'set -eu\n'
        f'd={shlex.quote(remote_dir)}\n'
        f'if [ -f "$d/manifest.json" ]; then\n'
        f'  sha256sum "$d/manifest.json" | cut -d" " -f1\n'
        f'else\n'
        f'  echo ABSENT\n'
        f'fi\n'
    )
    command = ssh_command(key, jump, target) + ["sh", "-s"]
    result = _read_probe(command, script, "manifest")
    answer = result.stdout.strip().splitlines()[-1] if result.stdout.strip() else ""
    if answer == "ABSENT":
        return ""
    if len(answer) != 64 or not all(c in "0123456789abcdef" for c in answer):
        raise RemoteError(f"node1 returned an unusable manifest digest: {answer[:80]!r}")
    return answer


def push_directory(staging: Path, *, key: Path, jump: str, target: str,
                   remote_dir: str, rate_kbps: int, timeout: int) -> dict:
    """Stream one prepared directory to node1 and return the remote summary.

    The unpack script is passed as a command argument, never on stdin: the
    remote shell and ``tar`` would otherwise both be reading the same pipe,
    and whichever consumed the archive bytes first would win.
    """
    if not staging.is_dir() or staging.is_symlink():
        raise RemoteError(f"staging directory is not a real directory: {staging}")

    tar = ["nice", "-n", "19", "ionice", "-c3",
           "tar", "-C", str(staging), "-cf", "-", "."]
    remote = [*ssh_command(key, jump, target),
              "bash", "-c", _remote_script(remote_dir)]

    sender = subprocess.Popen(tar, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert sender.stdout is not None
    stages = [sender]
    source = sender.stdout
    # pv is the usual rate limiter. It is not installed on these hosts today,
    # so an unthrottled stream is the fallback rather than a failed archive --
    # nice/ionice still keep the push behind the live workload.
    if rate_kbps > 0 and _has_pv():
        limiter = subprocess.Popen(["pv", "-q", "-L", f"{rate_kbps}k"],
                                   stdin=source, stdout=subprocess.PIPE)
        source.close()
        stages.append(limiter)
        source = limiter.stdout

    try:
        receiver = subprocess.run(remote, stdin=source, text=True,
                                  capture_output=True, check=False, timeout=timeout)
    finally:
        if source is not None:
            source.close()
        for stage in stages:
            try:
                stage.wait(timeout=60)
            except subprocess.TimeoutExpired:
                stage.kill()

    sender_stderr = sender.stderr.read().decode(errors="replace") if sender.stderr else ""
    if sender.returncode:
        raise RemoteError(f"tar failed while reading {staging}: {sender_stderr[-600:]}")
    if receiver.returncode:
        raise RemoteError("node1 rejected the stream: "
                          f"{(receiver.stderr or receiver.stdout)[-600:]}")
    try:
        summary = json.loads(receiver.stdout.strip().splitlines()[-1])
    except (ValueError, IndexError) as exc:
        raise RemoteError(f"node1 returned no JSON summary: {receiver.stdout[-300:]}") from exc
    if summary.get("ok") is not True:
        raise RemoteError(f"node1 reported failure: {summary.get('error', summary)}")
    return summary


def _has_pv() -> bool:
    return subprocess.run(["sh", "-c", "command -v pv"], capture_output=True,
                          check=False).returncode == 0
