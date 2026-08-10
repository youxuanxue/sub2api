#!/usr/bin/env python3
"""Fail-closed evaluator and live collector for one Edge platform cutover."""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import importlib.util
import json
import os
import pathlib
import shlex
import subprocess
import sys
import tempfile
import time
from typing import Any


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"
EC2_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
REMOTE_PROBE = REPO_ROOT / "ops/migration/probe-edge-platform-cutover.sh"
APP_RESOLVER = REPO_ROOT / "ops/lib/resolve-app-container.sh"
PHASES = ("candidate", "plan", "post-dns", "rollback-ready")
_MISSING = object()


def _iso_utc(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def _parse_time(value: object) -> dt.datetime:
    if not isinstance(value, str) or not value:
        raise ValueError("timestamp is required")
    normalized = f"{value[:-1]}+00:00" if value.endswith("Z") else value
    parsed = dt.datetime.fromisoformat(normalized)
    if parsed.tzinfo is None:
        raise ValueError("timestamp timezone is required")
    return parsed.astimezone(dt.timezone.utc)


def _path_get(data: dict[str, Any], path: str) -> object:
    value: object = data
    for part in path.split("."):
        if not isinstance(value, dict) or part not in value:
            return _MISSING
        value = value[part]
    return value


def evaluate_observation(
    phase: str,
    observation: dict[str, Any],
    now: dt.datetime,
) -> dict[str, Any]:
    """Evaluate a collected observation without performing any I/O."""
    if phase not in PHASES:
        raise ValueError(f"unsupported phase: {phase}")
    if now.tzinfo is None:
        raise ValueError("now must be timezone-aware")

    blockers: list[str] = []
    checks: list[dict[str, Any]] = []

    def block(code: str) -> None:
        if code not in blockers:
            blockers.append(code)

    def required(path: str) -> object:
        value = _path_get(observation, path)
        if value is _MISSING or value is None or value == "":
            block(f"missing:{path}")
            return _MISSING
        return value

    def check(code: str, ok: bool, actual: object = None) -> None:
        checks.append({"name": code, "ok": bool(ok), "actual": actual})
        if not ok:
            block(code)

    def require_bool(path: str, code: str) -> object:
        value = required(path)
        if value is _MISSING:
            return value
        if not isinstance(value, bool):
            block(f"invalid:{path}")
            return _MISSING
        check(code, value, value)
        return value

    edge_id = required("edge_id")
    ls_owner = required("matrix.lightsail_deployable")
    ec2_owner = required("matrix.ec2_deployable")
    candidate = required("matrix.ec2_migration_candidate")
    matrix_values = (ls_owner, ec2_owner, candidate)
    if all(isinstance(value, bool) for value in matrix_values):
        check("matrix_dual_owner", not (ls_owner and ec2_owner), matrix_values[:2])
        if phase in ("candidate", "plan"):
            check(
                "matrix_phase_state_invalid",
                matrix_values == (True, False, True),
                matrix_values,
            )
        elif phase == "post-dns":
            check(
                "matrix_phase_state_invalid",
                matrix_values == (False, True, False),
                matrix_values,
            )
        else:
            check("matrix_owner_missing", bool(ls_owner) ^ bool(ec2_owner), matrix_values[:2])
    else:
        for path, value in zip(
            (
                "matrix.lightsail_deployable",
                "matrix.ec2_deployable",
                "matrix.ec2_migration_candidate",
            ),
            matrix_values,
        ):
            if value is not _MISSING and not isinstance(value, bool):
                block(f"invalid:{path}")

    target_account_total: object = _MISSING
    if phase != "rollback-ready":
        require_bool("target.ssm_online", "target_ssm_offline")
        require_bool("target.docker_healthy", "target_docker_unhealthy")
        require_bool("target.health_ok", "target_local_health_failed")
        if phase == "post-dns":
            require_bool("target.public_health_ok", "target_public_health_failed")

        credits = required("target.cpu_credits")
        if credits is not _MISSING:
            check(
                "target_cpu_credits_not_unlimited",
                str(credits).lower() == "unlimited",
                credits,
            )
        instance_type = required("target.instance_type")
        if instance_type is not _MISSING:
            check("target_instance_type_mismatch", instance_type == "t4g.small", instance_type)
        required("target.app_tag")
        target_account_total = required("target.account_total")
        if not (
            isinstance(target_account_total, int)
            and not isinstance(target_account_total, bool)
            and target_account_total >= 0
        ) and target_account_total is not _MISSING:
            block("invalid:target.account_total")

    if phase in ("candidate", "plan"):
        schedulable = required("target.schedulable_accounts")
        if isinstance(schedulable, int) and not isinstance(schedulable, bool):
            check(
                "target_accounts_schedulable_before_cutover",
                schedulable == 0,
                schedulable,
            )
        elif schedulable is not _MISSING:
            block("invalid:target.schedulable_accounts")

        time_path = (
            "observation_started_at"
            if phase == "candidate"
            else "candidate_observation_started_at"
        )
        started = required(time_path)
        if started is not _MISSING:
            try:
                elapsed = (now.astimezone(dt.timezone.utc) - _parse_time(started)).total_seconds()
                check("candidate_observation_under_1h", elapsed >= 3600, int(elapsed))
            except ValueError:
                block(f"invalid:{time_path}")

    if phase == "plan" and isinstance(target_account_total, int) and target_account_total > 0:
        require_bool("target.oauth_model_smoke_ok", "target_oauth_model_smoke_failed")

    p0_p1 = required("alerts.p0_p1_open") if phase != "rollback-ready" else _MISSING
    if isinstance(p0_p1, int) and not isinstance(p0_p1, bool):
        check("active_p0_p1_alerts", p0_p1 == 0, p0_p1)
    elif p0_p1 is not _MISSING:
        block("invalid:alerts.p0_p1_open")

    def check_rollback_source() -> None:
        source_signals = []
        for path in ("source.ssm_online", "source.docker_healthy", "source.health_ok"):
            value = required(path)
            if value is not _MISSING and not isinstance(value, bool):
                block(f"invalid:{path}")
                value = False
            source_signals.append(value)
        if all(value is not _MISSING for value in source_signals):
            check("rollback_source_unavailable", all(source_signals), source_signals)

    if phase in ("plan", "post-dns", "rollback-ready"):
        check_rollback_source()

    if phase in ("plan", "rollback-ready"):
        source_ip = required("source_ipv4")
        rollback_ip = required("rollback_ipv4")
        if source_ip is not _MISSING and rollback_ip is not _MISSING:
            check("rollback_ipv4_mismatch", source_ip == rollback_ip, rollback_ip)

    if phase == "post-dns":
        if isinstance(target_account_total, int) and target_account_total > 0:
            require_bool("target.oauth_model_smoke_ok", "target_oauth_model_smoke_failed")

        target_ip = required("target_ipv4")
        authoritative = required("dns.authoritative_ipv4")
        public = required("dns.public_ipv4")
        if (
            target_ip is not _MISSING
            and authoritative is not _MISSING
            and public is not _MISSING
        ):
            dns_ok = (
                isinstance(authoritative, list)
                and isinstance(public, list)
                and authoritative == [target_ip]
                and public == [target_ip]
            )
            check(
                "dns_not_target_only",
                dns_ok,
                {"authoritative": authoritative, "public": public},
            )

        started = required("observation_started_at")
        if started is not _MISSING:
            try:
                elapsed = (now.astimezone(dt.timezone.utc) - _parse_time(started)).total_seconds()
                check("cutover_observation_under_10m", elapsed >= 600, int(elapsed))
            except ValueError:
                block("invalid:observation_started_at")

        source_requests = required("source.business_requests")
        if isinstance(source_requests, int) and not isinstance(source_requests, bool):
            check("source_business_traffic_present", source_requests == 0, source_requests)
        elif source_requests is not _MISSING:
            block("invalid:source.business_requests")

        target_total = target_account_total
        target_schedulable = required("target.schedulable_accounts")
        if all(
            isinstance(value, int) and not isinstance(value, bool)
            for value in (target_total, target_schedulable)
        ):
            check(
                "target_accounts_not_schedulable_after_cutover",
                target_total == 0 or target_schedulable > 0,
                {"total": target_total, "schedulable": target_schedulable},
            )

        if isinstance(target_total, int) and target_total > 0:
            target_requests = required("target.business_requests")
            target_served = required("target.served_requests")
            baseline_requests = required("baseline.business_requests")
            baseline_served = required("baseline.served_requests")
            ratio_values = (target_requests, target_served, baseline_requests, baseline_served)
            if all(
                isinstance(value, int) and not isinstance(value, bool)
                for value in ratio_values
            ):
                if target_requests <= 0 or baseline_requests <= 0:
                    block("served_ratio_unavailable")
                else:
                    current_ratio = target_served / target_requests
                    baseline_ratio = baseline_served / baseline_requests
                    check(
                        "served_ratio_drop_over_5pp",
                        baseline_ratio - current_ratio <= 0.05 + 1e-9,
                        {
                            "baseline": round(baseline_ratio, 6),
                            "current": round(current_ratio, 6),
                        },
                    )
                    check("target_business_traffic_missing", target_requests > 0, target_requests)
            else:
                for path, value in zip(
                    (
                        "target.business_requests",
                        "target.served_requests",
                        "baseline.business_requests",
                        "baseline.served_requests",
                    ),
                    ratio_values,
                ):
                    if value is not _MISSING:
                        block(f"invalid:{path}")

            baseline_p95 = required("baseline.p95_latency_ms")
            current_p95 = required("target.p95_latency_ms")
            if all(
                isinstance(value, (int, float)) and not isinstance(value, bool)
                for value in (baseline_p95, current_p95)
            ):
                check(
                    "p95_latency_over_2x_source",
                    baseline_p95 > 0 and current_p95 <= baseline_p95 * 2,
                    {"baseline_ms": baseline_p95, "current_ms": current_p95},
                )
            else:
                for path, value in (
                    ("baseline.p95_latency_ms", baseline_p95),
                    ("target.p95_latency_ms", current_p95),
                ):
                    if value is not _MISSING:
                        block(f"invalid:{path}")

        server_errors = required("target.server_errors")
        if isinstance(server_errors, int) and not isinstance(server_errors, bool):
            check("target_server_errors_present", server_errors == 0, server_errors)
        elif server_errors is not _MISSING:
            block("invalid:target.server_errors")

        recovery_required = required("alerts.recovery_required")
        if isinstance(recovery_required, bool) and recovery_required:
            edge_alert_open = required("alerts.edge_alert_open")
            delivery = required("alerts.feishu_recovery_delivered")
            if isinstance(edge_alert_open, bool):
                check("edge_alert_not_recovered", not edge_alert_open, edge_alert_open)
            elif edge_alert_open is not _MISSING:
                block("invalid:alerts.edge_alert_open")
            if isinstance(delivery, bool):
                check("feishu_recovery_missing", delivery, delivery)
            elif delivery is not _MISSING:
                block("invalid:alerts.feishu_recovery_delivered")
        elif recovery_required is not _MISSING and not isinstance(recovery_required, bool):
            block("invalid:alerts.recovery_required")

    return {
        "schema_version": 1,
        "edge_id": None if edge_id is _MISSING else edge_id,
        "phase": phase,
        "observed_at": _iso_utc(now),
        "checks": checks,
        "blockers": blockers,
    }


def _load_json(path: pathlib.Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise RuntimeError(f"expected JSON object: {path}")
    return value


def _aws_json(args: list[str]) -> dict[str, Any]:
    completed = subprocess.run(
        ["aws", *args, "--output", "json"],
        text=True,
        capture_output=True,
        check=False,
    )
    if completed.returncode != 0:
        detail = completed.stderr.strip().splitlines()[-1] if completed.stderr.strip() else "unknown"
        raise RuntimeError(f"aws {' '.join(args)}: {detail}")
    value = json.loads(completed.stdout)
    if not isinstance(value, dict):
        raise RuntimeError(f"aws {' '.join(args)} returned non-object JSON")
    return value


def _load_execution_module():
    path = REPO_ROOT / "ops/stage0/edge_ssm_execution.py"
    spec = importlib.util.spec_from_file_location("tk_cutover_edge_ssm_execution", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def _ssm_online(region: str, instance_id: str) -> bool:
    payload = _aws_json(
        [
            "ssm",
            "describe-instance-information",
            "--region",
            region,
            "--filters",
            f"Key=InstanceIds,Values={instance_id}",
        ],
    )
    rows = payload.get("InstanceInformationList")
    return (
        isinstance(rows, list)
        and len(rows) == 1
        and isinstance(rows[0], dict)
        and rows[0].get("PingStatus") == "Online"
    )


def _run_remote_probe(region: str, instance_id: str, since: str) -> dict[str, Any]:
    probe_b64 = base64.b64encode(REMOTE_PROBE.read_bytes()).decode("ascii")
    resolver_b64 = base64.b64encode(APP_RESOLVER.read_bytes()).decode("ascii")
    remote = " && ".join(
        (
            f"printf %s {shlex.quote(resolver_b64)} | base64 -d > /tmp/resolve-app-container.sh",
            "chmod 0755 /tmp/resolve-app-container.sh",
            f"printf %s {shlex.quote(probe_b64)} | base64 -d > /tmp/probe-edge-platform-cutover.sh",
            "chmod 0755 /tmp/probe-edge-platform-cutover.sh",
            f"SINCE={shlex.quote(since)} TK_LIB_DIR=/tmp bash /tmp/probe-edge-platform-cutover.sh",
        ),
    )
    payload = _aws_json(
        [
            "ssm",
            "send-command",
            "--region",
            region,
            "--instance-ids",
            instance_id,
            "--document-name",
            "AWS-RunShellScript",
            "--comment",
            "TokenKey read-only edge platform cutover probe",
            "--timeout-seconds",
            "120",
            "--parameters",
            json.dumps({"commands": [remote]}),
        ],
    )
    command_id = str((payload.get("Command") or {}).get("CommandId") or "")
    if not command_id:
        raise RuntimeError("SSM send-command returned no CommandId")

    deadline = time.monotonic() + 120
    invocation: dict[str, Any] = {}
    while time.monotonic() < deadline:
        try:
            invocation = _aws_json(
                [
                    "ssm",
                    "get-command-invocation",
                    "--region",
                    region,
                    "--command-id",
                    command_id,
                    "--instance-id",
                    instance_id,
                ],
            )
        except RuntimeError as exc:
            if "InvocationDoesNotExist" not in str(exc):
                raise
            time.sleep(2)
            continue
        status = invocation.get("Status")
        if status == "Success":
            break
        if status in ("Cancelled", "TimedOut", "Failed", "Cancelling"):
            raise RuntimeError(f"SSM probe failed with status {status}")
        time.sleep(2)
    else:
        raise RuntimeError("SSM probe timed out")

    stdout = str(invocation.get("StandardOutputContent") or "").strip()
    value = json.loads(stdout)
    if not isinstance(value, dict):
        raise RuntimeError("remote probe returned non-object JSON")
    return value


def _stack_output(region: str, stack: str, key: str) -> str:
    payload = _aws_json(
        ["cloudformation", "describe-stacks", "--region", region, "--stack-name", stack],
    )
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or len(stacks) != 1:
        raise RuntimeError(f"expected one stack: {stack}")
    for output in stacks[0].get("Outputs") or []:
        if isinstance(output, dict) and output.get("OutputKey") == key:
            return str(output.get("OutputValue") or "")
    raise RuntimeError(f"stack {stack} missing output {key}")


def _target_instance_facts(region: str, instance_id: str) -> tuple[str, str]:
    credits = _aws_json(
        [
            "ec2",
            "describe-instance-credit-specifications",
            "--region",
            region,
            "--instance-ids",
            instance_id,
        ],
    )
    credit_rows = credits.get("InstanceCreditSpecifications")
    credit_mode = ""
    if isinstance(credit_rows, list) and len(credit_rows) == 1:
        credit_mode = str(credit_rows[0].get("CpuCredits") or "")

    instances = _aws_json(
        ["ec2", "describe-instances", "--region", region, "--instance-ids", instance_id],
    )
    reservations = instances.get("Reservations")
    instance_type = ""
    if isinstance(reservations, list) and reservations:
        rows = reservations[0].get("Instances") or []
        if len(rows) == 1:
            instance_type = str(rows[0].get("InstanceType") or "")
    return credit_mode, instance_type


def _dig_ipv4(domain: str, server: str = "") -> list[str]:
    command = ["dig", "+short", "A", domain]
    if server:
        command.append(f"@{server}")
    completed = subprocess.run(command, text=True, capture_output=True, check=False)
    if completed.returncode != 0:
        raise RuntimeError(f"dig failed for {domain} via {server or 'system'}")
    return sorted(
        {
            line.strip()
            for line in completed.stdout.splitlines()
            if line.strip() and all(part.isdigit() for part in line.strip().split("."))
        },
    )


def _authoritative_ipv4(domain: str) -> list[str]:
    labels = domain.rstrip(".").split(".")
    if len(labels) < 2:
        raise RuntimeError(f"cannot derive DNS zone from {domain}")
    zone = ".".join(labels[-2:])
    completed = subprocess.run(
        ["dig", "+short", "NS", zone, "@1.1.1.1"],
        text=True,
        capture_output=True,
        check=False,
    )
    nameservers = sorted(line.rstrip(".") for line in completed.stdout.splitlines() if line.strip())
    if completed.returncode != 0 or not nameservers:
        raise RuntimeError(f"cannot resolve authoritative nameserver for {zone}")
    return _dig_ipv4(domain, nameservers[0])


def _public_health(domain: str, ipv4: str) -> bool:
    completed = subprocess.run(
        [
            "curl",
            "--silent",
            "--show-error",
            "--fail",
            "--max-time",
            "10",
            "--resolve",
            f"{domain}:443:{ipv4}",
            f"https://{domain}/health",
        ],
        text=True,
        capture_output=True,
        check=False,
    )
    return completed.returncode == 0


def collect_live(args: argparse.Namespace) -> dict[str, Any]:
    if not args.edge_id or not args.context:
        raise RuntimeError("live collection requires --edge-id and --context")
    context = _load_json(pathlib.Path(args.context))
    lightsail_data = _load_json(LIGHTSAIL_MATRIX)
    ec2_data = _load_json(EC2_MATRIX)
    source_target = (lightsail_data.get("targets") or {}).get(args.edge_id)
    target_target = (ec2_data.get("targets") or {}).get(args.edge_id)
    if not isinstance(source_target, dict) or not isinstance(target_target, dict):
        raise RuntimeError(f"edge {args.edge_id} must exist in both matrices")
    if source_target.get("domain") != target_target.get("domain"):
        raise RuntimeError(f"edge {args.edge_id} domain mismatch between matrices")

    execution = _load_execution_module()
    source_identity = execution.resolve_edge_execution_identity(
        REPO_ROOT,
        args.edge_id,
        platform="lightsail",
    )
    target_identity = execution.resolve_edge_execution_identity(
        REPO_ROOT,
        args.edge_id,
        platform="ec2",
    )
    source_probe = _run_remote_probe(
        source_identity.region,
        source_identity.instance_id,
        args.since,
    )
    target_probe = _run_remote_probe(
        target_identity.region,
        target_identity.instance_id,
        args.since,
    )
    source_probe["ssm_online"] = _ssm_online(
        source_identity.region,
        source_identity.instance_id,
    )
    target_probe["ssm_online"] = _ssm_online(
        target_identity.region,
        target_identity.instance_id,
    )
    credit_mode, instance_type = _target_instance_facts(
        target_identity.region,
        target_identity.instance_id,
    )
    target_probe["cpu_credits"] = credit_mode
    target_probe["instance_type"] = instance_type

    source_ipv4 = str(source_target.get("porkbun_a_ipv4") or "")
    target_ipv4 = _stack_output(target_identity.region, target_identity.ec2_stack, "PublicIP")
    if args.source_ip and args.source_ip != source_ipv4:
        raise RuntimeError(f"--source-ip does not match matrix for {args.edge_id}")
    if args.target_ip and args.target_ip != target_ipv4:
        raise RuntimeError(f"--target-ip does not match stack for {args.edge_id}")
    domain = target_identity.domain
    target_probe["public_health_ok"] = _public_health(domain, target_ipv4)

    context_target = context.get("target")
    if isinstance(context_target, dict) and "oauth_model_smoke_ok" in context_target:
        target_probe["oauth_model_smoke_ok"] = context_target["oauth_model_smoke_ok"]

    observation = {
        "schema_version": 1,
        "edge_id": args.edge_id,
        "source_ipv4": source_ipv4,
        "target_ipv4": target_ipv4,
        "rollback_ipv4": context.get("rollback_ipv4"),
        "observation_started_at": args.observation_started_at
        or context.get("observation_started_at"),
        "candidate_observation_started_at": args.candidate_observation_started_at
        or context.get("candidate_observation_started_at"),
        "matrix": {
            "lightsail_deployable": source_target.get("deployable") is True,
            "ec2_deployable": target_target.get("deployable") is True,
            "ec2_migration_candidate": target_target.get("migration_candidate") is True,
        },
        "dns": {
            "authoritative_ipv4": _authoritative_ipv4(domain),
            "public_ipv4": _dig_ipv4(domain, "1.1.1.1"),
        },
        "source": source_probe,
        "target": target_probe,
        "baseline": context.get("baseline"),
        "alerts": context.get("alerts"),
    }
    return observation


def _atomic_write(path: pathlib.Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            dir=path.parent,
            prefix=f".{path.name}.",
            delete=False,
        ) as handle:
            handle.write(value)
            temp_path = pathlib.Path(handle.name)
        os.replace(temp_path, path)
    finally:
        if temp_path is not None and temp_path.exists():
            temp_path.unlink()


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", required=True, choices=PHASES)
    parser.add_argument("--fixture", help="evaluate a collected JSON fixture; no network calls")
    parser.add_argument("--edge-id")
    parser.add_argument("--context", help="live smoke/baseline/alert receipt JSON")
    parser.add_argument("--source-ip", default="")
    parser.add_argument("--target-ip", default="")
    parser.add_argument("--observation-started-at", default="")
    parser.add_argument("--candidate-observation-started-at", default="")
    parser.add_argument("--since", default="15m")
    parser.add_argument("--output", help="atomically write the report to this path")
    parser.add_argument("--now", help="override current time for deterministic fixture tests")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        now = _parse_time(args.now) if args.now else dt.datetime.now(dt.timezone.utc)
        observation = (
            _load_json(pathlib.Path(args.fixture))
            if args.fixture
            else collect_live(args)
        )
        report = evaluate_observation(args.phase, observation, now)
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        print(f"::error::edge cutover collection failed: {exc}", file=sys.stderr)
        return 2

    rendered = json.dumps(report, indent=2, sort_keys=True) + "\n"
    print(rendered, end="")
    if args.output:
        _atomic_write(pathlib.Path(args.output), rendered)
    return 0 if not report["blockers"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
