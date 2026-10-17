#!/usr/bin/env python3
"""Live BBR check for admin/proxies egress hosts (Lightsail SSH).

Reads active proxy rows from prod (via run-probe + probe-list-proxies.sh),
maps each host IP to a Lightsail instance in the proxy region, then verifies
tcp_congestion_control=bbr and default_qdisc=fq over temporary Lightsail SSH.

This is intentionally separate from the static repo wiring check so CI/preflight
never needs Lightsail credentials or network path to egress proxies.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_REGION = "ap-northeast-1"
WANTED_CC = "bbr"
WANTED_QDISC = "fq"
PROXY_PROBE = REPO / "ops/observability/probe-list-proxies.sh"
RUN_PROBE = REPO / "ops/observability/run-probe.sh"

PROXY_LINE_RE = re.compile(r"^\{.*\}$")


def parse_proxy_probe_stdout(text: str) -> list[dict]:
    """Parse probe-list-proxies.sh stdout into proxy dicts."""
    out: list[dict] = []
    for raw in text.splitlines():
        line = raw.strip()
        if not PROXY_LINE_RE.match(line):
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(obj, dict):
            continue
        host = str(obj.get("host") or "").strip()
        if not host:
            continue
        out.append(
            {
                "id": obj.get("id"),
                "name": str(obj.get("name") or "").strip(),
                "host": host,
                "port": obj.get("port"),
                "status": str(obj.get("status") or "").strip(),
                "protocol": str(obj.get("protocol") or "").strip(),
            }
        )
    return out


def interpret_sysctl_output(text: str) -> tuple[str | None, str | None]:
    """Parse `sysctl -n` lines: first = cc, second = qdisc."""
    lines = [ln.strip() for ln in text.splitlines() if ln.strip()]
    cc = lines[0] if len(lines) >= 1 else None
    qdisc = lines[1] if len(lines) >= 2 else None
    return cc, qdisc


def bbr_ok(cc: str | None, qdisc: str | None) -> bool:
    return cc == WANTED_CC and qdisc == WANTED_QDISC


def map_hosts_to_lightsail(
    proxies: list[dict], instances: list[dict]
) -> tuple[dict[str, str], list[str]]:
    """Map proxy host IP → Lightsail instance name.

    instances: [{name, public_ip}]
    Returns (host→name, list of error strings for unmapped hosts).
    """
    by_ip = {}
    for inst in instances:
        ip = str(inst.get("public_ip") or "").strip()
        name = str(inst.get("name") or "").strip()
        if ip and name:
            by_ip[ip] = name
    mapping: dict[str, str] = {}
    errors: list[str] = []
    for p in proxies:
        host = p["host"]
        name = by_ip.get(host)
        if not name:
            errors.append(
                f"proxy id={p.get('id')} name={p.get('name')!r} host={host} "
                f"has no Lightsail instance with that public IP"
            )
            continue
        mapping[host] = name
    return mapping, errors


def aws_json(args: list[str]) -> dict | list:
    raw = subprocess.check_output(["aws", *args], text=True)
    return json.loads(raw)


def list_lightsail_instances(region: str) -> list[dict]:
    data = aws_json(
        [
            "lightsail",
            "get-instances",
            "--region",
            region,
            "--query",
            "instances[].{name:name,public_ip:publicIpAddress}",
            "--output",
            "json",
        ]
    )
    if not isinstance(data, list):
        raise RuntimeError("unexpected lightsail get-instances payload")
    return data


def fetch_proxies_via_run_probe(timeout_seconds: int = 90) -> list[dict]:
    if not RUN_PROBE.is_file() or not PROXY_PROBE.is_file():
        raise RuntimeError("missing run-probe or probe-list-proxies.sh")
    proc = subprocess.run(
        [
            "bash",
            str(RUN_PROBE),
            "--target",
            "prod",
            "--script",
            str(PROXY_PROBE),
            "--timeout-seconds",
            str(timeout_seconds),
            "--comment",
            "proxy-bbr-live list proxies",
        ],
        cwd=str(REPO),
        text=True,
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"run-probe list proxies failed rc={proc.returncode}: {proc.stderr[-800:]}"
        )
    proxies = parse_proxy_probe_stdout(proc.stdout)
    if not proxies:
        raise RuntimeError("no active proxies returned from prod (admin/proxies empty?)")
    return proxies


def lightsail_ssh_sysctl(instance_name: str, region: str) -> tuple[str | None, str | None, str]:
    """Return (cc, qdisc, raw_stdout) via temporary Lightsail SSH credentials."""
    details = aws_json(
        [
            "lightsail",
            "get-instance-access-details",
            "--instance-name",
            instance_name,
            "--protocol",
            "ssh",
            "--region",
            region,
        ]
    )
    access = details.get("accessDetails") or {}
    user = access.get("username")
    ip = access.get("ipAddress")
    private_key = access.get("privateKey")
    cert_key = access.get("certKey")
    if not (user and ip and private_key and cert_key):
        raise RuntimeError(f"incomplete Lightsail access details for {instance_name}")

    with tempfile.TemporaryDirectory(prefix="proxy-bbr-") as tmp:
        key_path = pathlib.Path(tmp) / "lightsail_key"
        cert_path = pathlib.Path(tmp) / "lightsail_key-cert.pub"
        key_path.write_text(private_key, encoding="utf-8")
        key_path.chmod(0o600)
        # Lightsail returns PKCS#8; convert so OpenSSH can sign the cert.
        subprocess.run(
            ["ssh-keygen", "-p", "-N", "", "-m", "PEM", "-f", str(key_path)],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        cert_path.write_text(
            cert_key if cert_key.endswith("\n") else cert_key + "\n", encoding="utf-8"
        )
        cert_path.chmod(0o600)
        remote = (
            "sysctl -n net.ipv4.tcp_congestion_control; "
            "sysctl -n net.core.default_qdisc"
        )
        cmd = [
            "ssh",
            "-i",
            str(key_path),
            "-o",
            f"CertificateFile={cert_path}",
            "-o",
            "IdentitiesOnly=yes",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
            "-o",
            "ConnectTimeout=20",
            f"{user}@{ip}",
            remote,
        ]
        proc = subprocess.run(cmd, text=True, capture_output=True, check=False)
        if proc.returncode != 0:
            raise RuntimeError(
                f"ssh to {instance_name} ({ip}) failed rc={proc.returncode}: "
                f"{(proc.stderr or proc.stdout)[-400:]}"
            )
        cc, qdisc = interpret_sysctl_output(proc.stdout)
        return cc, qdisc, proc.stdout


def check_proxies_live(
    *,
    region: str = DEFAULT_REGION,
    proxies: list[dict] | None = None,
    instances: list[dict] | None = None,
    ssh_sysctl=None,
) -> list[str]:
    """Return human-readable failure strings; empty list means all proxies OK.

    Injectable proxies/instances/ssh_sysctl keep unit tests offline.
    """
    errors: list[str] = []
    if proxies is None:
        proxies = fetch_proxies_via_run_probe()
    if instances is None:
        instances = list_lightsail_instances(region)
    mapping, map_errors = map_hosts_to_lightsail(proxies, instances)
    errors.extend(map_errors)

    checker = ssh_sysctl or (
        lambda name: lightsail_ssh_sysctl(name, region)[:2]
    )

    for p in proxies:
        host = p["host"]
        name = mapping.get(host)
        if not name:
            continue
        try:
            cc, qdisc = checker(name)
        except Exception as exc:  # noqa: BLE001 — surface per-host failure
            errors.append(
                f"proxy id={p.get('id')} name={p.get('name')!r} host={host} "
                f"instance={name}: {exc}"
            )
            continue
        if not bbr_ok(cc, qdisc):
            errors.append(
                f"proxy id={p.get('id')} name={p.get('name')!r} host={host} "
                f"instance={name}: cc={cc!r} qdisc={qdisc!r} "
                f"want cc={WANTED_CC!r} qdisc={WANTED_QDISC!r}"
            )
    return errors


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--region", default=os.environ.get("AWS_REGION", DEFAULT_REGION))
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()
    try:
        errors = check_proxies_live(region=args.region)
    except Exception as exc:  # noqa: BLE001
        print(f"FAIL: proxy BBR live check aborted: {exc}", file=sys.stderr)
        return 2
    if errors:
        if not args.quiet:
            print("FAIL: admin/proxies BBR live check:", file=sys.stderr)
            for e in errors:
                print(f"  - {e}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("ok: all active admin/proxies hosts have BBR (bbr+fq)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
