#!/usr/bin/env python3
"""Remove only the dormant default AWS media coordinates; never restart containers."""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile

import sys
# run-probe.sh ships resolve_app_container.py (and --with the matrix) to /tmp next
# to this file; a repo checkout finds them under ops/lib and deploy/tencent.
_HERE = Path(__file__).resolve().parent
sys.path[:0] = [str(_HERE), str(_HERE.parent / "lib")]
from resolve_app_container import resolve, resolve_postgres, resolve_redis  # noqa: E402

MATRIX_NAME = "candidate-usw-20261022.json"

LEGACY = {"MEDIA_STORAGE_DRIVER": "s3", "MEDIA_STORAGE_REGION": "us-east-1",
          "MEDIA_STORAGE_BUCKET": "tokenkey-prod-media-682751977094"}


def clean(text):
    lines = text.splitlines()
    values = {line.partition("=")[0]: line.partition("=")[2] for line in lines if "=" in line}
    if values.get("MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED", "false").lower() not in {"", "false", "0"}:
        raise RuntimeError("image_offload_enabled")
    present = {key: values[key] for key in LEGACY if key in values}
    if present and present != LEGACY:
        raise RuntimeError("custom_or_partial_media_configuration")
    return "\n".join(line for line in lines if line.partition("=")[0] not in LEGACY) + "\n"


def prod_identity(candidates=None):
    """Live prod hostname from the Tencent matrix (deploy/tencent SSOT), or raise.

    The retired Hetzner matrix named a host that no longer exists, so the old check
    could never pass. The Tencent matrix is the resolver's live-prod contract.
    """
    explicit = os.environ.get("TK_TENCENT_PROD_MATRIX", "").strip()
    paths = [Path(explicit)] if explicit else [
        _HERE / MATRIX_NAME, _HERE.parents[1] / "deploy" / "tencent" / MATRIX_NAME]
    for path in paths if candidates is None else candidates:
        try:
            target = json.loads(path.read_text())["target"]
        except (OSError, ValueError, KeyError, TypeError):
            continue
        if target.get("deployable") is not True or not target.get("hostname"):
            raise RuntimeError("prod_matrix_not_live")
        return target["hostname"]
    raise RuntimeError("prod_matrix_missing")


def run(args, *, purpose="readiness", env=None):
    result = subprocess.run(args, capture_output=True, text=True, timeout=30, env=env)
    if result.returncode:
        raise RuntimeError(purpose + "_probe_failed")
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    if os.environ.get("APPLY", "") not in {"", "yes-retire-dormant-media"}:
        raise RuntimeError("invalid_apply_guard")
    parser.add_argument("--apply", action="store_true", default=os.environ.get("APPLY") == "yes-retire-dormant-media")
    args = parser.parse_args()
    if socket.gethostname() != prod_identity():
        raise RuntimeError("prod_identity_mismatch")
    app, _ = resolve()
    if app is None:
        raise RuntimeError("live_app_unresolved")
    postgres, redis = resolve_postgres(), resolve_redis()
    if postgres is None or redis is None:
        raise RuntimeError("data_container_unresolved")
    env = json.loads(run(["docker", "inspect", "--format", "{{json .Config.Env}}", app], purpose="app_env"))
    if any(value.startswith("MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED=") and value.split("=", 1)[1].lower() not in {"", "false", "0"} for value in env):
        raise RuntimeError("live_image_offload_enabled")
    settings = run(["docker", "exec", "-e", "PGOPTIONS=-c default_transaction_read_only=on", postgres, "psql",
                    "-U", "tokenkey", "-d", "tokenkey", "-XAtc",
                    "SELECT count(*) FROM settings WHERE key='image_storage_config';"], purpose="settings")
    if settings != "0":
        raise RuntimeError("explicit_media_settings_require_review")
    # Match the completed read-only inventory: no live/legacy video task at all.
    values = dict(value.split("=", 1) for value in env if "=" in value)
    redis_env = dict(os.environ)
    redis_flags = ["-e", "REDISCLI_AUTH="]
    if values.get("REDIS_PASSWORD"):
        redis_env["REDISCLI_AUTH"] = values["REDIS_PASSWORD"]
        redis_flags = ["-e", "REDISCLI_AUTH"]
    else:
        redis_env.pop("REDISCLI_AUTH", None)
    # The Redis container may carry a stale REDISCLI_AUTH even when the server
    # has no password. Explicitly clear it rather than authenticating with it.
    keys = run(["docker", "exec", *redis_flags, redis, "sh", "-c",
                'if [ -z "${REDISCLI_AUTH:-}" ]; then unset REDISCLI_AUTH; fi; exec redis-cli "$@"',
                "sh", "-n", values.get("REDIS_DB", "0"),
                "--scan", "--pattern", "tk:newapi:video_task:*"], purpose="redis", env=redis_env)
    if keys:
        raise RuntimeError("video_tasks_require_review")
    path = Path("/var/lib/tokenkey/.env")
    original = path.read_text()
    replacement = clean(original)
    if args.apply and replacement != original:
        if os.geteuid() != 0:
            raise RuntimeError("root_required")
        backup = Path("/var/backups/tokenkey-media-config") / dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        backup.mkdir(mode=0o700, parents=True)
        (backup / "host.env").write_text(original)
        (backup / "host.env").chmod(0o600)
        fd, temp = tempfile.mkstemp(prefix=".media-env-", dir=path.parent)
        try:
            with os.fdopen(fd, "w") as handle:
                handle.write(replacement)
            os.replace(temp, path)
        finally:
            Path(temp).unlink(missing_ok=True)
    print(json.dumps({"operation": "apply" if args.apply else "plan", "dormant_defaults_removed": args.apply,
                      "gateway_restart": False, "running_container_env": "unchanged_until_normal_rollout"}))


if __name__ == "__main__":
    try:
        main()
    except RuntimeError as exc:
        print(json.dumps({"ok": False, "reason": str(exc)}))
        raise SystemExit(2)
