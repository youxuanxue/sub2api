#!/usr/bin/env python3
"""Warm every rolling app node and persist the receipt rolling.py requires.

The script is deliberately sequential and fail-stop. A failed warm never
touches the running container, and a later rolling deploy refuses to start
unless every app node has the exact local tag plus image-distribution receipt.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable

try:
    from deploy.domestic import remote_exec
except ImportError:  # run as a script from deploy/domestic/
    import remote_exec  # type: ignore

try:
    import image_distribution
except ImportError:  # imported as deploy.domestic.warm_fanout
    from deploy.domestic import image_distribution  # type: ignore

ROOT = Path("/var/lib/tokenkey")
LOCK = ROOT / "bluegreen-deploy.lock"
RECEIPTS = ROOT / "image-distribution"
LOCAL_REPO = "tokenkey-domestic/tokenkey"
TAG_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(-(rc|beta)\.[0-9]+)?$")
REV_RE = re.compile(r"^[0-9a-f]{40}$")
ARCH_RE = re.compile(r"^sha256:[a-f0-9]{64}$")


class WarmFanoutError(Exception):
    """A warm was refused or failed; no later node should be attempted."""


def utcnow() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def identity(tag: str, revision: str) -> str:
    return f"{LOCAL_REPO}:{tag}-{revision[:9]}"


def choose_channel(node: remote_exec.Node, requested: str) -> str:
    if requested == "auto":
        return "ssm" if node.ssm_instance_id else "tat"
    if requested not in ("tat", "ssm"):
        raise WarmFanoutError(f"channel must be auto, tat, or ssm (got {requested!r})")
    if requested == "ssm" and not node.ssm_instance_id:
        raise WarmFanoutError(f"{node.name}: --channel ssm requires ssm_instance_id")
    return requested


def build_ghcr_script(repository: str, tag: str, revision: str, digest: str = "") -> str:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]*(?::[0-9]+)?", repository):
        raise WarmFanoutError(f"invalid repository: {repository!r}")
    if digest and not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
        raise WarmFanoutError(f"invalid digest: {digest!r}")
    source = f"{repository}@{digest}" if digest else f"{repository}:{tag}"
    local = identity(tag, revision)
    script = r'''set -euo pipefail
umask 077
export SOURCE=__SOURCE__
export EXPECT_TAG=__TAG__
export EXPECT_REV=__REV__
export LOCAL_TAG=__LOCAL__
export RECEIPT=__RECEIPT__
export LOCK=__LOCK__
docker pull "$SOURCE"
python3 - <<'TOKENKEY_GHCR_WARM'
import fcntl
import json
import os
import subprocess
from pathlib import Path

source = os.environ["SOURCE"]
tag = os.environ["EXPECT_TAG"]
revision = os.environ["EXPECT_REV"]
local = os.environ["LOCAL_TAG"]


def inspect(ref):
    proc = subprocess.run(
        ["docker", "image", "inspect", ref],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode:
        return None
    items = json.loads(proc.stdout)
    return items[0] if items else None


def validated(image):
    labels = (image.get("Config") or {}).get("Labels") or {}
    got = (
        image.get("Architecture"),
        image.get("Os"),
        labels.get("org.opencontainers.image.version"),
        labels.get("org.opencontainers.image.revision"),
    )
    want = ("amd64", "linux", tag, revision)
    if got != want:
        raise SystemExit(
            f"image identity mismatch: got {got!r}, want {want!r}"
        )


state = Path(os.environ["RECEIPT"]).parent
state.mkdir(mode=0o700, parents=True, exist_ok=True)
with open(os.environ["LOCK"], "a") as lock_file:
    fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    current = inspect(local)
    cached = current is not None
    if current:
        validated(current)
        source_image = inspect(source)
        if source_image and source_image.get("RootFS", {}).get("Layers") != current.get("RootFS", {}).get("Layers"):
            raise SystemExit("existing local tag has different filesystem layers; refusing to replace it")
    else:
        source_image = inspect(source)
        if source_image is None:
            raise SystemExit(f"pulled image not found: {source}")
        validated(source_image)
        subprocess.run(["docker", "tag", source_image["Id"], local], check=True)
        current = source_image
    result = {
        "schema_version": 1,
        "tag": tag,
        "revision": revision,
        "source": source,
        "architecture": "amd64",
        "image_id": current["Id"],
        "local_tag": local,
        "cached": cached,
    }
    saved = Path(os.environ["RECEIPT"])
    temporary = saved.with_suffix(".tmp")
    temporary.write_text(json.dumps(result, sort_keys=True) + "\n")
    os.chmod(temporary, 0o600)
    os.replace(temporary, saved)
    print(json.dumps(result, sort_keys=True))
TOKENKEY_GHCR_WARM
'''
    replacements = {
        "__SOURCE__": json.dumps(source),
        "__TAG__": json.dumps(tag),
        "__REV__": json.dumps(revision),
        "__LOCAL__": json.dumps(local),
        "__RECEIPT__": json.dumps(str(RECEIPTS / f"{tag}-{revision}.json")),
        "__LOCK__": json.dumps(str(LOCK)),
    }
    for marker, value in replacements.items():
        script = script.replace(marker, value)
    return script


def last_json_object(text: str) -> dict:
    decoder = json.JSONDecoder()
    found = None
    for i, char in enumerate(text):
        if char != "{":
            continue
        try:
            value, _ = decoder.raw_decode(text[i:])
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            found = value
    if found is None:
        raise WarmFanoutError("remote warm produced no JSON receipt")
    return found


def validate_receipt(receipt: dict, tag: str, revision: str) -> dict:
    local = identity(tag, revision)
    expected = {
        "tag": tag,
        "revision": revision,
        "architecture": "amd64",
        "local_tag": local,
    }
    for key, value in expected.items():
        if receipt.get(key) != value:
            raise WarmFanoutError(f"warm receipt {key}={receipt.get(key)!r}, want {value!r}")
    if not ARCH_RE.fullmatch(str(receipt.get("image_id") or "")):
        raise WarmFanoutError("warm receipt image_id is missing or malformed")
    return receipt


def write_receipt(path: Path | None, payload: dict) -> None:
    if path is None:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def _validate_identity(tag: str, revision: str) -> None:
    if not TAG_RE.fullmatch(tag):
        raise WarmFanoutError(f"invalid tag: {tag!r}")
    if not REV_RE.fullmatch(revision):
        raise WarmFanoutError(f"invalid revision: {revision!r}")


def render_all(
    nodes: list[remote_exec.Node],
    *,
    tag: str,
    revision: str,
    transport: str,
    script_for: Callable[[remote_exec.Node], str],
    channel: str,
    render_out: Path,
) -> dict:
    """Render one signed prewarm script per app without dispatching it."""
    _validate_identity(tag, revision)
    if transport not in ("s3", "ghcr"):
        raise WarmFanoutError(f"transport must be s3 or ghcr (got {transport!r})")
    if not nodes:
        raise WarmFanoutError("manifest has no app nodes")
    payload = {
        "schema_version": 1,
        "kind": "warm-render",
        "tag": tag,
        "revision": revision,
        "transport": transport,
        "target_local_tag": identity(tag, revision),
        "created_at": utcnow(),
        "nodes": [],
    }
    for node in nodes:
        via = choose_channel(node, channel)
        script = script_for(node)
        if not isinstance(script, str) or not script.strip():
            raise WarmFanoutError(f"{node.name}: rendered script is empty")
        payload["nodes"].append({
            "name": node.name,
            "private_ip": node.private_ip,
            "channel": via,
            "script_sha256": hashlib.sha256(script.encode("utf-8")).hexdigest(),
            "script": script,
        })
    write_receipt(render_out, payload)
    return payload


def load_render(
    path: Path,
    nodes: list[remote_exec.Node],
    *,
    tag: str,
    revision: str,
    transport: str,
    channel: str,
) -> list[tuple[remote_exec.Node, str, str]]:
    """Validate a rendered artifact against the current manifest identity."""
    _validate_identity(tag, revision)
    if transport not in ("s3", "ghcr"):
        raise WarmFanoutError(f"transport must be s3 or ghcr (got {transport!r})")
    try:
        payload = json.loads(path.read_text())
    except (OSError, ValueError) as exc:
        raise WarmFanoutError(f"cannot read render file {path}: {exc}") from exc
    expected_root = {
        "schema_version": 1,
        "kind": "warm-render",
        "tag": tag,
        "revision": revision,
        "transport": transport,
        "target_local_tag": identity(tag, revision),
    }
    for key, value in expected_root.items():
        if payload.get(key) != value:
            raise WarmFanoutError(
                f"render {key}={payload.get(key)!r}, want {value!r}"
            )
    rendered = payload.get("nodes")
    if not isinstance(rendered, list) or len(rendered) != len(nodes):
        raise WarmFanoutError("rendered nodes do not match the manifest app list")
    result: list[tuple[remote_exec.Node, str, str]] = []
    for node, entry in zip(nodes, rendered):
        if not isinstance(entry, dict):
            raise WarmFanoutError(f"{node.name}: rendered node is not an object")
        expected_name = node.name
        expected_ip = node.private_ip
        expected_channel = choose_channel(node, channel)
        if entry.get("name") != expected_name or entry.get("private_ip") != expected_ip:
            raise WarmFanoutError(
                f"rendered node identity {entry.get('name')!r}/{entry.get('private_ip')!r}, "
                f"want {expected_name!r}/{expected_ip!r}"
            )
        if entry.get("channel") != expected_channel:
            raise WarmFanoutError(
                f"{node.name}: rendered channel={entry.get('channel')!r}, "
                f"want {expected_channel!r}"
            )
        script = entry.get("script")
        if not isinstance(script, str) or not script.strip():
            raise WarmFanoutError(f"{node.name}: rendered script is empty")
        digest = hashlib.sha256(script.encode("utf-8")).hexdigest()
        if entry.get("script_sha256") != digest:
            raise WarmFanoutError(f"{node.name}: rendered script sha256 mismatch")
        result.append((node, expected_channel, script))
    return result


def warm_all(
    nodes: list[remote_exec.Node],
    *,
    tag: str,
    revision: str,
    transport: str,
    script_for: Callable[[remote_exec.Node], str],
    channel: str,
    receipt_out: Path | None,
    timeout: int,
    runner: Callable[..., remote_exec.Result] = remote_exec.run,
) -> dict:
    _validate_identity(tag, revision)
    if transport not in ("s3", "ghcr"):
        raise WarmFanoutError(f"transport must be s3 or ghcr (got {transport!r})")
    if not nodes:
        raise WarmFanoutError("manifest has no app nodes")
    payload = {
        "schema_version": 1,
        "tag": tag,
        "revision": revision,
        "transport": transport,
        "target_local_tag": identity(tag, revision),
        "started_at": utcnow(),
        "finished_at": None,
        "outcome": "pending",
        "nodes": [],
        "errors": [],
    }
    try:
        for node in nodes:
            try:
                via = choose_channel(node, channel)
            except WarmFanoutError as exc:
                payload["errors"].append(str(exc))
                raise
            entry = {
                "name": node.name,
                "private_ip": node.private_ip,
                "channel": via,
                "status": "pending",
                "exit_code": None,
                "receipt": None,
            }
            payload["nodes"].append(entry)
            try:
                result = runner(
                    node,
                    script_for(node),
                    timeout=timeout,
                    via=via,
                )
            except Exception as exc:
                entry["status"] = "error"
                payload["errors"].append(f"{node.name}: {exc}")
                raise WarmFanoutError(f"{node.name}: warm dispatch failed: {exc}") from None
            entry["status"] = result.status
            entry["exit_code"] = result.exit_code
            if result.status != "Success":
                detail = (result.stderr_tail or result.stdout_tail or "").strip()[-600:]
                message = f"{node.name}: warm {result.status} (exit {result.exit_code})"
                if detail:
                    message += f": {detail}"
                payload["errors"].append(message)
                raise WarmFanoutError(message)
            try:
                receipt = validate_receipt(last_json_object(result.stdout_tail), tag, revision)
            except WarmFanoutError as exc:
                payload["errors"].append(f"{node.name}: {exc}")
                raise
            entry["status"] = "Success"
            entry["receipt"] = receipt
            print(
                f"warm_fanout: {node.name} channel={via} "
                f"image_id={receipt['image_id']} local_tag={receipt['local_tag']}",
                flush=True,
            )
        payload["outcome"] = "success"
        return payload
    except Exception:
        payload["outcome"] = "failed"
        raise
    finally:
        payload["finished_at"] = utcnow()
        write_receipt(receipt_out, payload)


def parser() -> argparse.ArgumentParser:
    here = Path(__file__).resolve().parent
    result = argparse.ArgumentParser(description=__doc__)
    sub = result.add_subparsers(dest="action", required=True)
    warm = sub.add_parser("warm-all", help="warm every app node and write rolling receipts")
    warm.add_argument("--manifest", type=Path, default=here / "prod-target.json")
    warm.add_argument("--tag", required=True)
    warm.add_argument("--revision", required=True)
    warm.add_argument("--transport", choices=("s3", "ghcr"), required=True)
    warm.add_argument("--channel", choices=("auto", "tat", "ssm"), default="auto")
    warm.add_argument("--repository", default="ghcr.io/youxuanxue/sub2api")
    warm.add_argument("--digest", default="")
    warm.add_argument("--timeout", type=int, default=1200)
    warm.add_argument("--receipt-out", type=Path)
    render = sub.add_parser("render-all", help="render one signed prewarm script per app node")
    render.add_argument("--manifest", type=Path, default=here / "prod-target.json")
    render.add_argument("--tag", required=True)
    render.add_argument("--revision", required=True)
    render.add_argument("--transport", choices=("s3", "ghcr"), required=True)
    render.add_argument("--channel", choices=("auto", "tat", "ssm"), default="auto")
    render.add_argument("--repository", default="ghcr.io/youxuanxue/sub2api")
    render.add_argument("--digest", default="")
    render.add_argument("--render-out", type=Path, required=True)
    dispatch = sub.add_parser("dispatch-rendered", help="dispatch a validated render artifact")
    dispatch.add_argument("--manifest", type=Path, default=here / "prod-target.json")
    dispatch.add_argument("--tag", required=True)
    dispatch.add_argument("--revision", required=True)
    dispatch.add_argument("--transport", choices=("s3", "ghcr"), required=True)
    dispatch.add_argument("--channel", choices=("auto", "tat", "ssm"), default="auto")
    dispatch.add_argument("--render-file", type=Path, required=True)
    dispatch.add_argument("--timeout", type=int, default=1200)
    dispatch.add_argument("--receipt-out", type=Path)
    return result


def main(
    argv: list[str] | None = None,
    *,
    runner: Callable[..., remote_exec.Result] | None = None,
) -> int:
    args = parser().parse_args(argv)
    try:
        normalized = args.tag[1:] if args.tag.startswith("v") else args.tag
        _validate_identity(normalized, args.revision)
        nodes = remote_exec.app_nodes(args.manifest)
        if args.action in ("warm-all", "render-all"):
            if args.transport == "ghcr":
                script_for = lambda _node: build_ghcr_script(
                    args.repository, normalized, args.revision, args.digest
                )
            else:
                import boto3
                config = image_distribution.settings()
                client = boto3.client("s3", region_name=config["region"])

                def script_for(_node: remote_exec.Node) -> str:
                    return image_distribution.render(
                        client, config["bucket"], normalized, args.revision, args.digest
                    )
        if args.action == "render-all":
            payload = render_all(
                nodes,
                tag=normalized,
                revision=args.revision,
                transport=args.transport,
                script_for=script_for,
                channel=args.channel,
                render_out=args.render_out,
            )
            print(json.dumps({
                "outcome": "success",
                "tag": payload["tag"],
                "revision": payload["revision"],
                "nodes": [
                    {"name": node["name"], "channel": node["channel"]}
                    for node in payload["nodes"]
                ],
            }, ensure_ascii=False))
            return 0
        if args.action == "dispatch-rendered":
            rendered = load_render(
                args.render_file,
                nodes,
                tag=normalized,
                revision=args.revision,
                transport=args.transport,
                channel=args.channel,
            )
            scripts = {node.name: (via, script) for node, via, script in rendered}
            payload = warm_all(
                nodes,
                tag=normalized,
                revision=args.revision,
                transport=args.transport,
                script_for=lambda node: scripts[node.name][1],
                channel=args.channel,
                receipt_out=args.receipt_out,
                timeout=args.timeout,
                runner=runner or remote_exec.run,
            )
        else:
            payload = warm_all(
                nodes,
                tag=normalized,
                revision=args.revision,
                transport=args.transport,
                script_for=script_for,
                channel=args.channel,
                receipt_out=args.receipt_out,
                timeout=args.timeout,
                runner=runner or remote_exec.run,
            )
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return 0
    except (WarmFanoutError, remote_exec.RemoteExecError) as exc:
        print(f"warm_fanout: error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
