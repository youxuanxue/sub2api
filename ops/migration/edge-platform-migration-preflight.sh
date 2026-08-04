#!/usr/bin/env bash
# Read-only readiness and cost preflight for the Lightsail -> EC2 edge migration.
set -euo pipefail

exec python3 - "$@" <<'PY'
from __future__ import annotations

import argparse
import datetime as dt
import ipaddress
import json
import math
import pathlib
import statistics
import subprocess
import sys
import tempfile
from decimal import Decimal, ROUND_HALF_UP
from typing import Any


EXPECTED_EDGES = ("us3", "us4", "us5", "us6")
REGIONS = ("us-east-2", "us-west-2")
EIP_QUOTA_CODE = "L-0263D0A3"
VPC_QUOTA_CODE = "L-F678F1CE"
AMI_PARAMETER = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
FIXED_EDGE_RAW = Decimal("19.115")
FIXED_EDGE_DISPLAY = Decimal("19.12")
NETWORK_OUT_USD_PER_GIB = Decimal("0.09")
CONTINGENCY_USD = Decimal("10.00")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Collect and evaluate read-only readiness for all active edge migrations.",
    )
    parser.add_argument("--fixture", help="evaluate a collected fixture instead of calling AWS/DNS")
    parser.add_argument("--matrix", default="deploy/aws/lightsail/edge-targets-lightsail.json")
    parser.add_argument("--format", choices=("json",), default="json")
    parser.add_argument("--output", help="also write the JSON receipt atomically to this path")
    return parser.parse_args()


def aws_json(args: list[str]) -> dict[str, Any]:
    command = ["aws", *args, "--output", "json"]
    completed = subprocess.run(command, text=True, capture_output=True, check=False)
    if completed.returncode != 0:
        detail = completed.stderr.strip().splitlines()[-1] if completed.stderr.strip() else "unknown error"
        raise RuntimeError(f"{' '.join(command[:-2])}: {detail}")
    value = json.loads(completed.stdout)
    if not isinstance(value, dict):
        raise RuntimeError(f"{' '.join(command[:-2])}: expected JSON object")
    return value


def dns_ipv4(domain: str) -> list[str]:
    completed = subprocess.run(
        ["dig", "+short", "A", domain, "@1.1.1.1"],
        text=True,
        capture_output=True,
        check=False,
    )
    if completed.returncode != 0:
        detail = completed.stderr.strip() or f"exit {completed.returncode}"
        raise RuntimeError(f"dig {domain}: {detail}")
    addresses: list[str] = []
    for line in completed.stdout.splitlines():
        candidate = line.strip()
        try:
            parsed = ipaddress.ip_address(candidate)
        except ValueError:
            continue
        if parsed.version == 4:
            addresses.append(candidate)
    return sorted(set(addresses))


def load_fleet(matrix_path: pathlib.Path) -> list[dict[str, Any]]:
    data = json.loads(matrix_path.read_text(encoding="utf-8"))
    targets = data.get("targets")
    if not isinstance(targets, dict):
        raise RuntimeError(f"invalid targets object in {matrix_path}")
    fleet: list[dict[str, Any]] = []
    for edge_id in sorted(targets):
        target = targets[edge_id]
        if not isinstance(target, dict) or target.get("deployable") is not True:
            continue
        fleet.append(
            {
                "edge_id": edge_id,
                "region": target.get("lightsail_region"),
                "domain": target.get("domain"),
                "expected_ipv4": target.get("porkbun_a_ipv4"),
                "instance_name": target.get("instance_name"),
                "ssm_prefix": target.get("ssm_prefix"),
            },
        )
    return fleet


def metric_points(
    edge: dict[str, Any],
    *,
    metric_name: str,
    period: int,
    start_time: dt.datetime,
    end_time: dt.datetime,
    unit: str,
    statistic: str,
) -> list[float]:
    payload = aws_json(
        [
            "lightsail",
            "get-instance-metric-data",
            "--region",
            str(edge["region"]),
            "--instance-name",
            str(edge["instance_name"]),
            "--metric-name",
            metric_name,
            "--period",
            str(period),
            "--start-time",
            start_time.isoformat(),
            "--end-time",
            end_time.isoformat(),
            "--unit",
            unit,
            "--statistics",
            statistic,
        ],
    )
    points = payload.get("metricData")
    if not isinstance(points, list):
        raise RuntimeError(f"{edge['edge_id']} {metric_name}: missing metricData")
    key = statistic[0].lower() + statistic[1:]
    values: list[float] = []
    for point in points:
        if isinstance(point, dict) and isinstance(point.get(key), (int, float)):
            values.append(float(point[key]))
    return values


def percentile_nearest_rank(values: list[float], percentile: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    rank = max(1, math.ceil(percentile * len(ordered)))
    return ordered[rank - 1]


def collect_live(matrix_path: pathlib.Path) -> dict[str, Any]:
    fleet = load_fleet(matrix_path)
    collected: dict[str, Any] = {
        "fleet": fleet,
        "quotas": {},
        "network_out_30d": {},
        "cpu_24h": {},
        "dns": {},
        "amis": {},
        "instance_type_offerings": {},
        "ssm": {},
        "collection_errors": [],
    }
    errors: list[str] = collected["collection_errors"]
    now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)

    for region in REGIONS:
        quota_row: dict[str, Any] = {}
        quota_calls = (
            ("eip_limit", "ec2", EIP_QUOTA_CODE),
            ("vpc_limit", "vpc", VPC_QUOTA_CODE),
        )
        for output_key, service_code, quota_code in quota_calls:
            try:
                payload = aws_json(
                    [
                        "service-quotas",
                        "get-service-quota",
                        "--region",
                        region,
                        "--service-code",
                        service_code,
                        "--quota-code",
                        quota_code,
                    ],
                )
                quota_row[output_key] = payload.get("Quota", {}).get("Value")
            except (RuntimeError, json.JSONDecodeError) as exc:
                errors.append(f"quota:{region}:{output_key}:{exc}")
        count_calls = (
            ("eip_used", ["ec2", "describe-addresses", "--region", region], "Addresses"),
            ("vpc_used", ["ec2", "describe-vpcs", "--region", region], "Vpcs"),
        )
        for output_key, command, response_key in count_calls:
            try:
                payload = aws_json(command)
                rows = payload.get(response_key)
                if not isinstance(rows, list):
                    raise RuntimeError(f"missing {response_key}")
                quota_row[output_key] = len(rows)
            except (RuntimeError, json.JSONDecodeError) as exc:
                errors.append(f"quota:{region}:{output_key}:{exc}")
        collected["quotas"][region] = quota_row

        try:
            offering = aws_json(
                [
                    "ec2",
                    "describe-instance-type-offerings",
                    "--region",
                    region,
                    "--location-type",
                    "region",
                    "--filters",
                    "Name=instance-type,Values=t4g.small",
                ],
            )
            rows = offering.get("InstanceTypeOfferings")
            collected["instance_type_offerings"][region] = sorted(
                {
                    str(row.get("InstanceType"))
                    for row in rows or []
                    if isinstance(row, dict) and row.get("InstanceType")
                },
            )
        except (RuntimeError, json.JSONDecodeError) as exc:
            errors.append(f"offering:{region}:{exc}")

        try:
            parameter = aws_json(
                [
                    "ssm",
                    "get-parameter",
                    "--region",
                    region,
                    "--name",
                    AMI_PARAMETER,
                ],
            )
            image_id = parameter.get("Parameter", {}).get("Value")
            if not image_id:
                raise RuntimeError("public AL2023 ARM64 parameter is empty")
            images = aws_json(
                [
                    "ec2",
                    "describe-images",
                    "--region",
                    region,
                    "--image-ids",
                    str(image_id),
                ],
            )
            image_rows = images.get("Images")
            if not isinstance(image_rows, list) or len(image_rows) != 1:
                raise RuntimeError(f"expected one image for {image_id}")
            collected["amis"][region] = {
                "image_id": image_id,
                "architecture": image_rows[0].get("Architecture"),
            }
        except (RuntimeError, json.JSONDecodeError) as exc:
            errors.append(f"ami:{region}:{exc}")

    for edge in fleet:
        edge_id = str(edge.get("edge_id") or "")
        region = str(edge.get("region") or "")
        try:
            values = metric_points(
                edge,
                metric_name="NetworkOut",
                period=86400,
                start_time=now - dt.timedelta(days=30),
                end_time=now,
                unit="Bytes",
                statistic="Sum",
            )
            collected["network_out_30d"][edge_id] = round(sum(values) / (1024**3), 3)
        except (RuntimeError, json.JSONDecodeError, KeyError) as exc:
            errors.append(f"network_out_30d:{edge_id}:{exc}")

        try:
            values = metric_points(
                edge,
                metric_name="CPUUtilization",
                period=3600,
                start_time=now - dt.timedelta(hours=24),
                end_time=now,
                unit="Percent",
                statistic="Average",
            )
            collected["cpu_24h"][edge_id] = {
                "average_pct": round(statistics.fmean(values), 3) if values else None,
                "p95_pct": round(percentile_nearest_rank(values, 0.95), 3) if values else None,
            }
        except (RuntimeError, json.JSONDecodeError, KeyError) as exc:
            errors.append(f"cpu_24h:{edge_id}:{exc}")

        try:
            domain = str(edge.get("domain") or "")
            collected["dns"][edge_id] = {
                "expected_ipv4": edge.get("expected_ipv4"),
                "resolved_ipv4": dns_ipv4(domain),
            }
        except RuntimeError as exc:
            errors.append(f"dns:{edge_id}:{exc}")

        try:
            prefix = str(edge.get("ssm_prefix") or "").rstrip("/")
            parameter = aws_json(
                [
                    "ssm",
                    "get-parameter",
                    "--region",
                    region,
                    "--name",
                    f"{prefix}/ssm_managed_instance_id",
                ],
            )
            instance_id = parameter.get("Parameter", {}).get("Value")
            if not instance_id:
                raise RuntimeError("managed instance parameter is empty")
            info = aws_json(
                [
                    "ssm",
                    "describe-instance-information",
                    "--region",
                    region,
                    "--filters",
                    f"Key=InstanceIds,Values={instance_id}",
                ],
            )
            rows = info.get("InstanceInformationList")
            ping_status = rows[0].get("PingStatus") if isinstance(rows, list) and rows else None
            collected["ssm"][edge_id] = {
                "instance_id": instance_id,
                "ping_status": ping_status,
            }
        except (RuntimeError, json.JSONDecodeError) as exc:
            errors.append(f"ssm:{edge_id}:{exc}")

    return collected


def numeric(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def money(value: Decimal) -> float:
    return float(value.quantize(Decimal("0.01"), rounding=ROUND_HALF_UP))


def evaluate(raw: dict[str, Any]) -> dict[str, Any]:
    blockers: list[str] = []
    fleet_raw = raw.get("fleet")
    fleet = [row for row in fleet_raw if isinstance(row, dict)] if isinstance(fleet_raw, list) else []
    fleet = sorted(fleet, key=lambda row: str(row.get("edge_id") or ""))
    edge_ids = [str(row.get("edge_id") or "") for row in fleet]
    if tuple(edge_ids) != EXPECTED_EDGES:
        blockers.append(f"fleet:expected={','.join(EXPECTED_EDGES)}:actual={','.join(edge_ids)}")

    quotas = raw.get("quotas") if isinstance(raw.get("quotas"), dict) else {}
    for region in REGIONS:
        row = quotas.get(region) if isinstance(quotas.get(region), dict) else {}
        for resource in ("eip", "vpc"):
            limit = row.get(f"{resource}_limit")
            used = row.get(f"{resource}_used")
            if not numeric(limit) or not numeric(used) or float(limit) - float(used) < 2:
                blockers.append(f"quota:{resource}:{region}:requires_2_spare")

    offerings = (
        raw.get("instance_type_offerings")
        if isinstance(raw.get("instance_type_offerings"), dict)
        else {}
    )
    amis = raw.get("amis") if isinstance(raw.get("amis"), dict) else {}
    for region in REGIONS:
        region_offerings = offerings.get(region)
        if not isinstance(region_offerings, list) or "t4g.small" not in region_offerings:
            blockers.append(f"offering:{region}:t4g.small_unavailable")
        ami = amis.get(region) if isinstance(amis.get(region), dict) else {}
        if not ami.get("image_id") or ami.get("architecture") != "arm64":
            blockers.append(f"ami:{region}:missing_or_not_arm64")

    network_out = raw.get("network_out_30d") if isinstance(raw.get("network_out_30d"), dict) else {}
    cpu_24h = raw.get("cpu_24h") if isinstance(raw.get("cpu_24h"), dict) else {}
    dns = raw.get("dns") if isinstance(raw.get("dns"), dict) else {}
    ssm = raw.get("ssm") if isinstance(raw.get("ssm"), dict) else {}
    forecast_per_edge: dict[str, int] = {}
    fixed_per_edge: dict[str, float] = {}
    for edge in fleet:
        edge_id = str(edge.get("edge_id") or "")
        fixed_per_edge[edge_id] = money(FIXED_EDGE_RAW)
        traffic = network_out.get(edge_id)
        if not numeric(traffic) or float(traffic) < 0:
            blockers.append(f"network_out_30d:{edge_id}:missing_or_invalid")
        else:
            forecast = FIXED_EDGE_DISPLAY + NETWORK_OUT_USD_PER_GIB * Decimal(str(traffic)) + CONTINGENCY_USD
            forecast_per_edge[edge_id] = math.ceil(forecast)
        cpu = cpu_24h.get(edge_id)
        if not isinstance(cpu, dict) or not numeric(cpu.get("average_pct")) or not numeric(cpu.get("p95_pct")):
            blockers.append(f"cpu_24h:{edge_id}:missing_or_invalid")
        dns_row = dns.get(edge_id) if isinstance(dns.get(edge_id), dict) else {}
        expected_ip = edge.get("expected_ipv4")
        resolved = dns_row.get("resolved_ipv4")
        if dns_row.get("expected_ipv4") != expected_ip or resolved != [expected_ip]:
            blockers.append(f"dns:{edge_id}:matrix_mismatch")
        ssm_row = ssm.get(edge_id) if isinstance(ssm.get(edge_id), dict) else {}
        if not ssm_row.get("instance_id") or ssm_row.get("ping_status") != "Online":
            blockers.append(f"ssm:{edge_id}:not_online")

    collection_errors = raw.get("collection_errors")
    if isinstance(collection_errors, list):
        for error in collection_errors:
            blockers.append(f"collection:{error}")

    blockers = sorted(set(blockers))
    return {
        "schema_version": 1,
        "generated_at": dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat(),
        "fleet": fleet,
        "quotas": quotas,
        "network_out_30d": network_out,
        "cpu_24h": cpu_24h,
        "dns": dns,
        "amis": amis,
        "instance_type_offerings": offerings,
        "ssm": ssm,
        "fixed_monthly_usd": {
            "per_edge": fixed_per_edge,
            "fleet": money(FIXED_EDGE_RAW * len(fleet)),
        },
        "forecast_monthly_usd": {
            "per_edge": forecast_per_edge,
            "fleet": sum(forecast_per_edge.values()) if len(forecast_per_edge) == len(fleet) else None,
        },
        "blockers": blockers,
    }


def write_atomic(path: pathlib.Path, payload: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
        handle.write(payload)
        handle.write("\n")
        temporary = pathlib.Path(handle.name)
    temporary.replace(path)


def main() -> int:
    args = parse_args()
    try:
        if args.fixture:
            raw = json.loads(pathlib.Path(args.fixture).read_text(encoding="utf-8"))
        else:
            raw = collect_live(pathlib.Path(args.matrix))
        if not isinstance(raw, dict):
            raise RuntimeError("collector input must be a JSON object")
        report = evaluate(raw)
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": 1,
            "generated_at": dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat(),
            "fleet": [],
            "quotas": {},
            "network_out_30d": {},
            "cpu_24h": {},
            "dns": {},
            "amis": {},
            "instance_type_offerings": {},
            "ssm": {},
            "fixed_monthly_usd": {"per_edge": {}, "fleet": 0.0},
            "forecast_monthly_usd": {"per_edge": {}, "fleet": None},
            "blockers": [f"collector:{exc}"],
        }
    payload = json.dumps(report, indent=2, sort_keys=True, ensure_ascii=True)
    print(payload)
    if args.output:
        write_atomic(pathlib.Path(args.output), payload)
    return 0 if not report["blockers"] else 1


raise SystemExit(main())
PY
