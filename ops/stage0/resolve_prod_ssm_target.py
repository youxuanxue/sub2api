#!/usr/bin/env python3
"""Resolve Stage0 prod SSM control-plane target.

``--target auto`` selects the live prod matrix: Tencent USW when
``deploy/tencent/candidate-usw-20261022.json`` has ``deployable=true``; otherwise
it fails closed. ``PROD_SSM_TARGET`` may explicitly select modes; retired
``aws`` / ``aws-fresh`` (Tokyo, retired 2026-10-23) / ``hetzner`` are rejected. No silent EC2 fallback when inventory fails.

Daily TAT / diagnostics trust the matrix Hybrid IDs (no AWS SSM inventory).
Break-glass ``TOKENKEY_REMOTE_VIA=ssm`` still requires a live Online Hybrid.
See ``trust_matrix_identity``.

Fleet narrative SSOT still lives at ``deploy/hetzner/README.md`` (historical
path name); the Hetzner Cloud prod matrix is retired and never resolves live.
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "ops/stage0"))
from bluegreen_host_runtime import volume_contract
TENCENT_USW_MATRIX = REPO_ROOT / "deploy/tencent/candidate-usw-20261022.json"
AWS_STACK = "tokenkey-prod-stage0"
INSTANCE_RE = re.compile(r"^(?:i|mi)-[0-9a-f]{8,}$")
ALLOWED_ENV_TARGETS = ("aws", "aws-fresh", "hetzner", "tencent-usw-candidate")


def fail(message: str) -> None:
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def gha_quote(value: object) -> str:
    return str(value).replace("%", "%25").replace("\n", "%0A").replace("\r", "%0D")


def aws_json(args: list[str]) -> Any:
    try:
        out = subprocess.check_output(["aws", *args, "--output", "json"], text=True)
    except subprocess.CalledProcessError as exc:
        fail(f"aws {' '.join(args)} failed: {exc}")
    if not out.strip():
        return None
    return json.loads(out)


def tencent_matrix_is_live_prod(path: pathlib.Path = TENCENT_USW_MATRIX) -> bool:
    try:
        target = load_tencent_usw_matrix(path)
    except SystemExit:
        return False
    return target.get("deployable") is True and target.get("resolvable") is True


def resolve_mode(explicit: str) -> str:
    if explicit and explicit != "auto":
        return explicit
    env = (os.environ.get("PROD_SSM_TARGET") or "").strip().lower()
    if env in ALLOWED_ENV_TARGETS:
        return env
    if env and env != "auto":
        fail(
            "PROD_SSM_TARGET must be auto|aws|aws-fresh|hetzner|tencent-usw-candidate "
            f"(got {env!r})"
        )
    # After 2026-10-22 cutover, live prod is Tencent when matrix.deployable=true.
    if tencent_matrix_is_live_prod():
        return "tencent-usw-candidate"
    fail(
        "auto: Tencent USW matrix is not deployable/resolvable; refusing to fall back "
        "to a retired target. Fix deploy/tencent/candidate-usw-20261022.json."
    )


def with_browser_origin(outputs: dict[str, str]) -> dict[str, str]:
    api_url = outputs["api_url"]
    api_host = api_url.removeprefix("https://")
    browser_origin = api_url
    if api_host.startswith("api."):
        browser_origin = f"https://{api_host[len('api.') :]}"
    out = dict(outputs)
    out["id"] = outputs["instance_id"]
    out["browser_origin"] = browser_origin
    return out


def resolve_aws(stack: str) -> dict[str, str]:
    fail(
        "target=aws is retired: old US prod instances were deleted; "
        "use auto (Tencent USW) for live prod"
    )


def resolve_fresh_aws() -> dict[str, str]:
    fail(
        "target=aws-fresh is retired: Tokyo i-045f062ce33e1a5af was stopped and its "
        "inventory removed on 2026-10-23; use auto (Tencent USW) for live prod"
    )


def list_instance_information(region: str) -> list[dict[str, Any]]:
    """Paginate DescribeInstanceInformation for Hybrid/Lightsail inventory."""
    rows: list[dict[str, Any]] = []
    next_token = ""
    while True:
        args = [
            "ssm",
            "describe-instance-information",
            "--region",
            region,
            "--max-results",
            "50",
        ]
        if next_token:
            args.extend(["--next-token", next_token])
        page = aws_json(args) or {}
        batch = page.get("InstanceInformationList") or []
        if isinstance(batch, list):
            rows.extend(r for r in batch if isinstance(r, dict))
        next_token = str(page.get("NextToken") or "")
        if not next_token:
            break
    return rows


def resolve_hetzner() -> dict[str, str]:
    """Hetzner Cloud prod is retired; never resolve a live Hybrid identity."""
    fail(
        "target=hetzner is retired: Hetzner Cloud prod was decommissioned; "
        "use auto (Tencent USW) for live prod. "
        "Fleet SSOT remains under deploy/hetzner/README.md (historical path name)."
    )


def load_tencent_usw_matrix_file(path: pathlib.Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        fail(f"invalid tencent usw matrix root: {path}")
    return data


def load_tencent_usw_matrix(path: pathlib.Path) -> dict[str, Any]:
    data = load_tencent_usw_matrix_file(path)
    target = data.get("target")
    if not isinstance(target, dict):
        fail(f"invalid tencent usw candidate matrix: {path}")
    return target


def list_fleet_node_names(matrix_path: pathlib.Path) -> list[str]:
    data = load_tencent_usw_matrix_file(matrix_path)
    names: list[str] = []
    entry = data.get("entry")
    if isinstance(entry, dict) and entry.get("name"):
        names.append(str(entry["name"]))
    apps = data.get("apps")
    if isinstance(apps, list):
        for app in apps:
            if isinstance(app, dict) and app.get("name"):
                names.append(str(app["name"]))
    data_machine = data.get("data_machine")
    if isinstance(data_machine, dict):
        names.append("data")
    return names


def resolve_node_selector(explicit: str) -> str:
    """Return fleet node name from --node or PROD_SSM_NODE; empty if unset."""
    if explicit.strip():
        return explicit.strip()
    return (os.environ.get("PROD_SSM_NODE") or "").strip()


def trust_matrix_identity() -> bool:
    """Whether resolve may return matrix Hybrid IDs without AWS SSM inventory.

    Daily diagnostics and TAT remotes already treat the fleet matrix as SSOT
    (``TOKENKEY_REMOTE_VIA=tat``, default). Hybrid ``DescribeInstanceInformation``
    requires AWS credentials the tencent-ops diagnose job deliberately lacks, so
    trusting the matrix keeps GHA → TAT read-only without reintroducing OIDC.

    Break-glass ``TOKENKEY_REMOTE_VIA=ssm`` still requires a live Online Hybrid.
    ``TOKENKEY_RESOLVE_TRUST_MATRIX=1|0`` forces either side explicitly.
    """
    explicit = (os.environ.get("TOKENKEY_RESOLVE_TRUST_MATRIX") or "").strip().lower()
    if explicit in ("1", "true", "yes"):
        return True
    if explicit in ("0", "false", "no"):
        return False
    via = (os.environ.get("TOKENKEY_REMOTE_VIA") or "tat").strip().lower()
    return via != "ssm"


def _matrix_instance(expected: str, *, name_hint: str = "") -> dict[str, Any]:
    if not expected.startswith("mi-") or not INSTANCE_RE.match(expected):
        fail(f"expected Hybrid mi-* for tencent node, got {expected!r}")
    return {
        "InstanceId": expected,
        "Name": name_hint,
        "ComputerName": name_hint,
        "PingStatus": "TrustedMatrix",
    }


def _online_instance(region: str, expected: str, *, name_hint: str = "") -> dict[str, Any]:
    if not expected.startswith("mi-") or not INSTANCE_RE.match(expected):
        fail(f"expected Hybrid mi-* for tencent node, got {expected!r}")
    if trust_matrix_identity():
        return _matrix_instance(expected, name_hint=name_hint)
    rows = list_instance_information(region)
    matches = [r for r in rows if str(r.get("InstanceId") or "") == expected]
    if not matches and name_hint:
        matches = [
            r
            for r in rows
            if str(r.get("ComputerName") or "") == name_hint or str(r.get("Name") or "") == name_hint
        ]
    if not matches:
        fail(f"no Hybrid instance {expected!r} (hint={name_hint!r}) in {region}")
    online = [r for r in matches if str(r.get("PingStatus") or "") == "Online"]
    if len(online) != 1:
        fail(f"expected exactly one Online Hybrid for {expected!r}; found {len(online)}")
    chosen = online[0]
    instance_id = str(chosen.get("InstanceId") or "")
    if instance_id != expected:
        fail(f"tencent SSM id mismatch: got {instance_id!r} expected {expected!r}")
    return chosen


def resolve_tencent_usw_node(matrix_path: pathlib.Path, node: str) -> dict[str, str]:
    """Resolve one fleet node (entry|app-*|data). No silent single-app default."""
    data = load_tencent_usw_matrix_file(matrix_path)
    target = data.get("target")
    if not isinstance(target, dict):
        fail(f"invalid tencent usw candidate matrix: {matrix_path}")
    if target.get("resolvable") is not True:
        fail("tencent usw matrix is not resolvable")
    live = target.get("deployable") is True
    region = str(data.get("ssm_region") or target.get("ssm_region") or "").strip()
    if not region:
        fail("tencent usw matrix missing ssm_region")

    if live:
        api_domain = str(target.get("api_domain") or "api.tokenkey.dev")
        if not api_domain.endswith(".tokenkey.dev") and not api_domain.endswith(".callmodel.io"):
            fail(f"live tencent prod api_domain invalid: {api_domain!r}")
        if target.get("cutover_completed") is not True and target.get("cutover_authorized") is not True:
            fail("live tencent prod requires cutover_completed or cutover_authorized")
    else:
        directed = str(target.get("directed_domain") or "")
        if not directed:
            fail("tencent usw candidate matrix missing directed_domain")
        if directed.endswith(".tokenkey.dev") or directed.startswith("api."):
            fail("tencent candidate must not use a live tokenkey.dev API domain")
        if target.get("cutover_authorized") is True:
            fail("pre-cutover candidate must keep cutover_authorized=false")
        api_domain = directed

    expected = ""
    name_hint = ""
    role = ""
    if node == "entry":
        entry = data.get("entry")
        if not isinstance(entry, dict):
            fail("matrix missing entry")
        expected = str(entry.get("ssm_instance_id") or "").strip()
        name_hint = str(entry.get("name") or "entry")
        role = "entry"
    elif node == "data":
        dm = data.get("data_machine")
        if not isinstance(dm, dict):
            fail("matrix missing data_machine")
        expected = str(dm.get("expected_ssm_instance_id") or dm.get("ssm_instance_id") or "").strip()
        name_hint = str(dm.get("ssm_name") or dm.get("hostname") or "data")
        role = "data"
    else:
        apps = data.get("apps")
        if not isinstance(apps, list):
            fail("matrix missing apps[]")
        match = next((a for a in apps if isinstance(a, dict) and str(a.get("name") or "") == node), None)
        if match is None:
            available = ", ".join(list_fleet_node_names(matrix_path)) or "(none)"
            fail(f"unknown --node {node!r}; available: {available}")
        expected = str(match.get("ssm_instance_id") or "").strip()
        name_hint = str(match.get("name") or node)
        role = "app"
    if not expected:
        fail(f"node {node!r} missing ssm_instance_id in matrix")

    chosen = _online_instance(region, expected, name_hint=name_hint)
    try:
        storage = volume_contract(target)
    except ValueError as exc:
        fail(str(exc))
    return with_browser_origin(
        {
            "target": "tencent-usw-candidate" if not live else "tencent-usw-prod",
            "instance_id": str(chosen.get("InstanceId") or expected),
            "ssm_region": region,
            "api_url": f"https://{api_domain}",
            "deploy_profile": "prod" if live else "tencent-usw-candidate",
            "platform": "tencent-lighthouse",
            "computer_name": str(chosen.get("Name") or chosen.get("ComputerName") or name_hint),
            "fleet_node": node,
            "fleet_role": role,
            "cutover_authorized": "true" if live else "false",
            "volume_mount": str(target.get("volume_mount") or ""),
            "volume_platform": storage["platform"],
            "volume_mapper": storage["mapper_name"],
            "volume_root_uuid": storage.get("root_uuid", ""),
        }
    )


def resolve_tencent_usw_candidate(matrix_path: pathlib.Path, node: str = "") -> dict[str, str]:
    """Resolve Tencent USW.

    Multi-app live matrices require an explicit fleet node (``--node`` /
    ``PROD_SSM_NODE``). Legacy single-target rows without ``apps[]`` keep the
    historical ``target.expected_ssm_instance_id`` path.
    """
    data = load_tencent_usw_matrix_file(matrix_path)
    apps = data.get("apps")
    has_apps = isinstance(apps, list) and len(apps) > 0
    selected = resolve_node_selector(node)
    if has_apps:
        if not selected:
            available = ", ".join(list_fleet_node_names(matrix_path)) or "(none)"
            fail(
                "prod fleet has apps[]; refusing silent single-app default. "
                f"Pass --node <name> or PROD_SSM_NODE (available: {available}). "
                "Fleet-wide diagnosis uses run-probe --all-apps."
            )
        return resolve_tencent_usw_node(matrix_path, selected)
    if selected:
        return resolve_tencent_usw_node(matrix_path, selected)

    target = load_tencent_usw_matrix(matrix_path)
    if target.get("resolvable") is not True:
        fail("tencent usw matrix is not resolvable")
    live = target.get("deployable") is True
    name = str(target.get("computer_name") or target.get("instance_name") or "")
    region = str(target.get("ssm_region") or "")
    expected = str(target.get("expected_ssm_instance_id") or "")
    if not name or not region or not expected:
        fail("tencent usw matrix missing computer_name/ssm_region/expected_ssm_instance_id")

    if live:
        api_domain = str(target.get("api_domain") or "api.tokenkey.dev")
        if not api_domain.endswith(".tokenkey.dev") and not api_domain.endswith(".callmodel.io"):
            fail(f"live tencent prod api_domain invalid: {api_domain!r}")
        if target.get("cutover_completed") is not True and target.get("cutover_authorized") is not True:
            fail("live tencent prod requires cutover_completed or cutover_authorized")
    else:
        directed = str(target.get("directed_domain") or "")
        if not directed:
            fail("tencent usw candidate matrix missing directed_domain")
        if directed.endswith(".tokenkey.dev") or directed.startswith("api."):
            fail("tencent candidate must not use a live tokenkey.dev API domain")
        if target.get("cutover_authorized") is True:
            fail("pre-cutover candidate must keep cutover_authorized=false")
        api_domain = directed

    try:
        storage = volume_contract(target)
    except ValueError as exc:
        fail(str(exc))
    if trust_matrix_identity():
        chosen = _matrix_instance(expected, name_hint=name)
    else:
        rows = list_instance_information(region)
        matches = [
            r
            for r in rows
            if str(r.get("ComputerName") or "") == name or str(r.get("Name") or "") == name
        ]
        if not matches:
            fail(f"no Hybrid instance with ComputerName/Name={name!r} in {region}")
        online = [r for r in matches if str(r.get("PingStatus") or "") == "Online"]
        if len(online) != 1:
            fail(f"expected exactly one Online Hybrid instance for {name!r}; found {len(online)}")
        chosen = online[0]
        instance_id = str(chosen.get("InstanceId") or "")
        if instance_id != expected:
            fail(f"tencent SSM id mismatch: got {instance_id!r} expected {expected!r}")
        if not instance_id.startswith("mi-") or not INSTANCE_RE.match(instance_id):
            fail(f"expected Hybrid mi-* for tencent, got {instance_id!r}")
    instance_id = str(chosen.get("InstanceId") or expected)
    if not instance_id.startswith("mi-") or not INSTANCE_RE.match(instance_id):
        fail(f"expected Hybrid mi-* for tencent, got {instance_id!r}")
    return with_browser_origin(
        {
            "target": "tencent-usw-candidate" if not live else "tencent-usw-prod",
            "instance_id": instance_id,
            "ssm_region": region,
            "api_url": f"https://{api_domain}",
            "deploy_profile": "prod" if live else "tencent-usw-candidate",
            "platform": "tencent-lighthouse",
            "computer_name": name,
            "cutover_authorized": "true" if live else "false",
            "volume_mount": str(target.get("volume_mount") or ""),
            "volume_platform": storage['platform'],
            "volume_mapper": storage['mapper_name'],
            "volume_root_uuid": storage.get('root_uuid', ''),
        }
    )


def write_github_output(path: str, outputs: dict[str, str]) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as fh:
        for key, value in outputs.items():
            fh.write(f"{key}={gha_quote(value)}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve Stage0 prod SSM target.")
    parser.add_argument(
        "--target",
        choices=("auto", "aws", "aws-fresh", "hetzner", "tencent-usw-candidate"),
        default="auto",
        help="auto → tencent when matrix.deployable=true; aws/aws-fresh/hetzner retired",
    )
    parser.add_argument("--stack", default=AWS_STACK)
    parser.add_argument("--matrix", default="")
    parser.add_argument(
        "--node",
        default="",
        help="fleet node: entry|app-old|app-cand|data (or PROD_SSM_NODE). "
        "Required when matrix has apps[].",
    )
    parser.add_argument(
        "--format",
        choices=("kv", "instance-id", "json"),
        default="kv",
    )
    parser.add_argument("--github-output", default="")
    args = parser.parse_args()

    mode = resolve_mode(args.target)
    if mode == "aws-fresh":
        resolve_fresh_aws()
    elif mode == "aws":
        outputs = resolve_aws(args.stack)
    elif mode == "tencent-usw-candidate":
        matrix = pathlib.Path(args.matrix) if args.matrix else TENCENT_USW_MATRIX
        outputs = resolve_tencent_usw_candidate(matrix, node=args.node)
    else:
        resolve_hetzner()

    if args.format == "instance-id":
        print(outputs["instance_id"])
    elif args.format == "json":
        print(json.dumps(outputs, sort_keys=True))
    else:
        for key, value in outputs.items():
            print(f"{key}={value}")
    write_github_output(args.github_output, outputs)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
