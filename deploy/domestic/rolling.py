#!/usr/bin/env python3
"""cn-prod rolling deploy across single-container app nodes.

Contract: docs/approved/design-cn-prod-rolling-deploy.md §4 (rolling), §5 (lock),
"不变量" and "验收". Only the post-M2/M4 layout is supported: one `tokenkey`
container per app node on <private_ip>:8080 and 127.0.0.1:18080, compose files
compose.yml + compose.override.yml + compose.local.yml + compose.node.yml under
/opt/tokenkey, image pinned by TOKENKEY_IMAGE= in a 0600 .env.

The same file runs in two roles:

* operator / CI side: `plan` (default), `deploy`, `status`. Remote work goes
  through an Executor that wraps remote_exec.run/put_file.
* host side: `agent <op>`. Shipped to /opt/tokenkey/releases/artifacts-rolling/
  and verified by sha256 before every call. Host code uses argv lists (no
  shell), never prints environment values and never reads .env beyond the
  TOKENKEY_IMAGE line.

Stdlib only.
"""
from __future__ import annotations

import argparse
import datetime as _dt
import fcntl
import getpass
import gzip
import hashlib
import json
import os
import re
import shlex
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Protocol

MIN_ROLLING_VERSION = "1.8.377"  # first image with start-draining marker + SIGUSR2

LOCAL_REPO = "tokenkey-domestic/tokenkey"
CONTAINER = "tokenkey"
LOOPBACK_PORT = 18080
ROOT = Path("/opt/tokenkey")
CONTROLS = Path("/opt/tokenkey/releases/artifacts-rolling")
AGENT_PATH = CONTROLS / "rolling.py"
SMOKE_PATH = CONTROLS / "candidate_smoke.py"
MARKER = Path("/var/lib/tokenkey/app/start-draining")
DATA_MOUNT = ("/var/lib/tokenkey/app", "/app/data")
LEASE = Path("/var/lib/tokenkey/rolling-deploy.lease")
IMAGE_RECEIPTS = Path("/var/lib/tokenkey/image-distribution")
COMPOSE_FILES = ("compose.yml", "compose.override.yml", "compose.local.yml", "compose.node.yml")
FORBIDDEN_CONTAINERS = ("tokenkey-blue", "tokenkey-green", "tokenkey-entry-origin", "tokenkey-caddy")
CREDENTIAL_PARITY_KEYS = ("DATABASE_PASSWORD", "REDIS_PASSWORD")
MIGRATION_STEP = {"app-b": "M2", "app-a": "M4"}

LEASE_TTL = 30 * 60
DRAIN_SETTLE = 10
DRAIN_MAX = 600
DRAIN_INTERVAL = 5
TRAFFIC_WINDOW = 120
CPU_LIMIT = 70.0
MEM_LIMIT = 60.0

TAG_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(-(rc|beta)\.[0-9]+)?$")
REV_RE = re.compile(r"^[0-9a-f]{40}$")
PROJECT_RE = re.compile(r"^[a-z0-9][a-z0-9_-]*$")
RESULT_PREFIX = "ROLLING_RESULT "

EXIT_OK, EXIT_FAIL, EXIT_USAGE, EXIT_WARN = 0, 1, 2, 3

# Layout kinds (docs/approved/design-prod-entry-multi-app.md §5):
# single-container = rolling-ready (cn-prod today). stage0-bluegreen = prod
# transitional; operator plan/deploy must refuse until apps are single-container.
LAYOUT_SINGLE = "single-container"
LAYOUT_BLUEGREEN = "stage0-bluegreen"


@dataclass(frozen=True)
class Layout:
    kind: str
    root: Path
    controls: Path


class RollingError(Exception):
    """Refusal or failure with an operator-facing message (never secrets)."""


def layout_for_manifest(manifest: dict) -> Layout:
    """Resolve host paths from the fleet manifest without touching live hosts."""
    raw = manifest.get("layout")
    if raw is None:
        # cn-prod manifests shipped before the layout field: single-container.
        return Layout(LAYOUT_SINGLE, Path("/opt/tokenkey"),
                      Path("/opt/tokenkey/releases/artifacts-rolling"))
    if not isinstance(raw, dict):
        raise RollingError("manifest.layout must be an object")
    kind = raw.get("kind")
    if kind not in (LAYOUT_SINGLE, LAYOUT_BLUEGREEN):
        raise RollingError(f"unsupported layout.kind: {kind!r}")
    root = Path(raw.get("root") or (
        "/opt/tokenkey" if kind == LAYOUT_SINGLE else "/var/lib/tokenkey"))
    controls = Path(raw.get("controls") or (str(root) + "/releases/artifacts-rolling"))
    return Layout(kind, root, controls)


def require_rolling_layout(manifest: dict) -> Layout:
    """Gate plan/deploy: only single-container fleets may roll."""
    layout = layout_for_manifest(manifest)
    if layout.kind != LAYOUT_SINGLE:
        raise RollingError(
            f"layout.kind={layout.kind!r} is not rolling-ready; "
            "migrate apps to single-container (Phase C) before rolling "
            "(design-prod-entry-multi-app.md §5)")
    for app in manifest.get("apps") or []:
        if not isinstance(app, dict):
            continue
        mem = app.get("memory_limit")
        if not mem:
            raise RollingError(f"apps[].memory_limit required for rolling ({app.get('name')})")
    return layout


def apply_layout(layout: Layout) -> None:
    """Bind module paths for host-side agent helpers (tests patch ROOT)."""
    global ROOT, CONTROLS, AGENT_PATH, SMOKE_PATH
    ROOT = layout.root
    CONTROLS = layout.controls
    AGENT_PATH = CONTROLS / "rolling.py"
    SMOKE_PATH = CONTROLS / "candidate_smoke.py"


# --------------------------------------------------------------------------
# shared helpers
# --------------------------------------------------------------------------

def normalize_tag(tag: str) -> str:
    tag = (tag or "").strip()
    if tag.startswith("v"):
        tag = tag[1:]
    if not TAG_RE.match(tag):
        raise RollingError(f"tag must be X.Y.Z (optionally -rc.N / -beta.N), got: {tag!r}")
    return tag


def require_revision(revision: str) -> str:
    if not REV_RE.match(revision or ""):
        raise RollingError("revision must be the full 40-hex commit sha")
    return revision


def parse_version(value: str) -> tuple[int, int, int, int, int]:
    m = re.fullmatch(r"v?(\d+)\.(\d+)\.(\d+)(?:-(rc|beta)\.(\d+))?", (value or "").strip())
    if not m:
        raise ValueError(f"not a release version: {value!r}")
    major, minor, patch, pre, num = m.groups()
    rank = {None: 2, "rc": 1, "beta": 0}[pre]
    return int(major), int(minor), int(patch), rank, int(num or 0)


def version_at_least(value: str, floor: str = MIN_ROLLING_VERSION) -> bool:
    try:
        return parse_version(value) >= parse_version(floor)
    except ValueError:
        return False


def local_tag(tag: str, revision: str) -> str:
    return f"{LOCAL_REPO}:{tag}-{revision[:9]}"


def backup_name(tag: str, revision: str) -> str:
    return f".env.before-rolling-{tag}-{revision[:9]}"


def short_id(image_id: str | None) -> str:
    if not image_id:
        return "-"
    return image_id.removeprefix("sha256:")[:12]


def utcnow() -> _dt.datetime:
    return _dt.datetime.now(_dt.timezone.utc)


def iso(ts: _dt.datetime) -> str:
    return ts.replace(microsecond=0).isoformat().replace("+00:00", "Z")


def parse_iso(value: str) -> _dt.datetime:
    return _dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


# --------------------------------------------------------------------------
# host side (agent)
# --------------------------------------------------------------------------

def _emit(result: dict) -> int:
    sys.stdout.write(RESULT_PREFIX + json.dumps(result, sort_keys=True) + "\n")
    return 0


def _docker(*args: str, check: bool = True, timeout: int = 120) -> subprocess.CompletedProcess:
    proc = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=timeout)
    if check and proc.returncode != 0:
        raise RollingError(f"docker {args[0]} failed (exit {proc.returncode}): {proc.stderr.strip()[-300:]}")
    return proc


def _inspect(kind: str, ref: str) -> dict | None:
    proc = _docker(kind, "inspect", ref, check=False) if kind == "image" else _docker("inspect", ref, check=False)
    if proc.returncode != 0:
        return None
    data = json.loads(proc.stdout)
    return data[0] if data else None


_NO_PROXY_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def _http(url: str, timeout: float = 5) -> tuple[int, str]:
    try:
        with _NO_PROXY_OPENER.open(url, timeout=timeout) as resp:
            return resp.status, resp.read(2048).decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read(2048).decode("utf-8", "replace")
    except Exception:  # connection refused, reset, timeout
        return 0, ""


def _health() -> int:
    return _http(f"http://127.0.0.1:{LOOPBACK_PORT}/health")[0]


def _inflight() -> dict | None:
    proc = _docker("exec", CONTAINER, "wget", "-q", "-T", "3", "-O", "-",
                   "http://localhost:8080/health/inflight", check=False, timeout=15)
    if proc.returncode != 0:
        return None
    try:
        body = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return None
    n = body.get("in_flight")
    if not isinstance(n, int) or isinstance(n, bool) or n < 0:
        return None
    return {"in_flight": n, "draining": body.get("draining") is True}


def _env_pins(env: Path) -> list[str]:
    return [line.partition("=")[2].strip() for line in env.read_text().splitlines()
            if line.startswith("TOKENKEY_IMAGE=")]


def _write_private(path: Path, text: str) -> None:
    tmp = path.with_name(path.name + ".rolling-tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        fh.write(text)
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def _compose_args(project: str) -> list[str]:
    args = ["compose", "-p", project, "--project-directory", str(ROOT)]
    for name in COMPOSE_FILES:
        args += ["-f", str(ROOT / name)]
    return args


def _credential_drift(project: str, live_env: list[str]) -> tuple[list[str] | None, str | None]:
    proc = _docker(*_compose_args(project), "config", "--format", "json", check=False)
    if proc.returncode != 0:
        # compose errors name variables, not values; still keep only the exit code.
        return None, f"compose config failed (exit {proc.returncode})"
    service = (json.loads(proc.stdout).get("services") or {}).get(CONTAINER) or {}
    rendered = service.get("environment") or {}
    if not isinstance(rendered, dict):
        return None, "compose service environment must be a mapping"
    live = dict(item.split("=", 1) for item in live_env if "=" in item)
    drift = [k for k in CREDENTIAL_PARITY_KEYS
             if k in rendered and k in live and str(rendered[k]) != live[k]]
    return drift, None


def _container_facts(c: dict) -> dict:
    cfg, state, host = c.get("Config") or {}, c.get("State") or {}, c.get("HostConfig") or {}
    labels = cfg.get("Labels") or {}
    command = " ".join([c.get("Path") or ""] + list(c.get("Args") or []))
    ports = []
    for binding in ((c.get("NetworkSettings") or {}).get("Ports") or {}).get("8080/tcp") or []:
        ports.append(f"{binding.get('HostIp')}:{binding.get('HostPort')}")
    mounts = {(m.get("Source"), m.get("Destination"), m.get("RW")) for m in c.get("Mounts") or []}
    return {
        "image_ref": cfg.get("Image", ""),
        "image_id": c.get("Image", ""),
        "running": state.get("Running") is True,
        "socat": "socat" in (cfg.get("Image", "") + " " + command).lower(),
        "project": labels.get("com.docker.compose.project", ""),
        "config_files": labels.get("com.docker.compose.project.config_files", ""),
        "ports": sorted(ports),
        "data_mount": (DATA_MOUNT[0], DATA_MOUNT[1], True) in mounts,
        "memory_limit": int(host.get("Memory") or 0),
    }


def _image_labels(ref: str) -> dict | None:
    img = _inspect("image", ref)
    if img is None:
        return None
    labels = (img.get("Config") or {}).get("Labels") or {}
    return {"image_id": img.get("Id", ""), "architecture": img.get("Architecture", ""),
            "version": labels.get("org.opencontainers.image.version", ""),
            "revision": labels.get("org.opencontainers.image.revision", "")}


def _vitals(c: dict | None) -> dict:
    out = {"health": _health(), "running": bool(c and (c.get("State") or {}).get("Running")),
           "cpu_percent": None, "mem_percent": None, "memory_limit": 0}
    if not out["running"]:
        return out
    host = c.get("HostConfig") or {}
    out["memory_limit"] = int(host.get("Memory") or 0)
    proc = _docker("stats", "--no-stream", "--format", "{{json .}}", CONTAINER, check=False, timeout=30)
    if proc.returncode == 0 and proc.stdout.strip():
        stats = json.loads(proc.stdout.strip().splitlines()[-1])
        cpus = (int(host.get("NanoCpus") or 0) / 1e9) or (os.cpu_count() or 1)
        out["cpu_percent"] = round(float(stats.get("CPUPerc", "0").rstrip("%")) / cpus, 2)
        out["mem_percent"] = float(stats.get("MemPerc", "0").rstrip("%"))
    return out


def agent_probe(args) -> dict:
    env = ROOT / ".env"
    facts: dict[str, Any] = {
        "compose_files": {name: (ROOT / name).is_file() for name in COMPOSE_FILES},
        "env": {"exists": env.is_file(), "mode": None, "pins": []},
        "marker": MARKER.exists(),
    }
    if env.is_file():
        facts["env"]["mode"] = oct(env.stat().st_mode & 0o777)[2:]
        facts["env"]["pins"] = _env_pins(env)
    ps = _docker("ps", "-a", "--format", "{{.Names}}|{{.State}}")
    facts["containers"] = dict(line.split("|", 1) for line in ps.stdout.split() if "|" in line)
    raw = _inspect("container", CONTAINER) if CONTAINER in facts["containers"] else None
    facts["container"] = _container_facts(raw) if raw else None
    facts["current_image"] = _image_labels(raw["Image"]) if raw else None
    pins = facts["env"]["pins"]
    pin_img = _image_labels(pins[0]) if len(pins) == 1 else None
    facts["pin_image_id"] = pin_img["image_id"] if pin_img else None
    facts.update(_vitals(raw))
    facts["inflight"] = _inflight() if facts["running"] else None
    facts["target"] = None
    facts["backup"] = None
    if args.tag:
        tag, revision = args.tag, args.revision
        ltag = local_tag(tag, revision)
        target = _image_labels(ltag)
        if target:
            target["local_tag"] = ltag
            try:
                saved = json.loads((IMAGE_RECEIPTS / f"{tag}-{revision}.json").read_text())
            except (OSError, json.JSONDecodeError):
                saved = {}
            target["receipt_image_id"] = saved.get("image_id")
            target["receipt_local_tag"] = saved.get("local_tag")
        facts["target"] = target
        backup = ROOT / backup_name(tag, revision)
        if backup.is_file():
            bpins = _env_pins(backup)
            bimg = _image_labels(bpins[0]) if len(bpins) == 1 else None
            facts["backup"] = {"pin": bpins[0] if len(bpins) == 1 else None,
                               "image_id": bimg["image_id"] if bimg else None,
                               "revision": bimg["revision"] if bimg else None,
                               "version": bimg["version"] if bimg else None}
    facts["credential_drift"], facts["compose_error"] = None, None
    c = facts["container"]
    if raw and c["project"] and PROJECT_RE.match(c["project"]) and all(facts["compose_files"].values()):
        facts["credential_drift"], facts["compose_error"] = _credential_drift(
            c["project"], (raw.get("Config") or {}).get("Env") or [])
    return facts


def agent_vitals(args) -> dict:
    return _vitals(_inspect("container", CONTAINER))


def agent_signal(args) -> dict:
    _docker("kill", "-s", args.signal, CONTAINER)
    return {"signal": args.signal}


def agent_drain(args) -> dict:
    _docker("kill", "-s", "USR1", CONTAINER)
    time.sleep(args.settle)
    health = _health()
    deadline = time.monotonic() + args.max_wait
    tries, last = 0, None
    while True:
        tries += 1
        last = _inflight()
        if last and last["draining"] and last["in_flight"] == 0:
            return {"drained": True, "health": health, "in_flight": 0, "tries": tries}
        if time.monotonic() >= deadline:
            break
        time.sleep(args.interval)
    return {"drained": False, "health": health, "in_flight": (last or {}).get("in_flight"), "tries": tries}


def agent_repin(args) -> dict:
    env = ROOT / ".env"
    backup = ROOT / backup_name(args.tag, args.revision)
    target = local_tag(args.tag, args.revision)
    lines = env.read_text().splitlines(keepends=True)
    pins = [ln for ln in lines if ln.startswith("TOKENKEY_IMAGE=")]
    if len(pins) != 1:
        raise RollingError(".env must contain exactly one TOKENKEY_IMAGE line")
    current = pins[0].partition("=")[2].strip()
    if current == target:
        if not backup.is_file():
            raise RollingError(".env already pins the target but no rolling backup exists; cannot roll back")
    else:
        _write_private(backup, "".join(lines))
    _write_private(env, "".join(f"TOKENKEY_IMAGE={target}\n" if ln.startswith("TOKENKEY_IMAGE=") else ln
                                for ln in lines))
    backup_pins = _env_pins(backup)
    return {"backup": str(backup), "old_ref": backup_pins[0] if backup_pins else None, "new_ref": target}


def agent_restore(args) -> dict:
    env = ROOT / ".env"
    backup = ROOT / backup_name(args.tag, args.revision)
    if not backup.is_file():
        raise RollingError(f"rolling backup {backup} missing")
    pins = _env_pins(backup)
    if pins != [args.expect_ref]:
        raise RollingError("rolling backup pin does not match the recorded old image; refusing restore")
    _write_private(env, backup.read_text())
    return {"restored": str(backup), "ref": args.expect_ref}


def agent_marker(args) -> dict:
    if args.remove:
        MARKER.unlink(missing_ok=True)
    else:
        MARKER.parent.mkdir(parents=True, exist_ok=True)
        MARKER.touch(mode=0o644, exist_ok=True)
    return {"marker": MARKER.exists()}


def agent_up(args) -> dict:
    if not PROJECT_RE.match(args.project):
        raise RollingError("invalid compose project name")
    proc = _docker(*_compose_args(args.project), "up", "-d", "--no-deps", CONTAINER, check=False, timeout=300)
    if proc.returncode != 0:
        return {"ok": False, "error": f"compose up failed (exit {proc.returncode})",
                "running": False, "health": 0, "image_id": None}
    deadline = time.monotonic() + args.wait
    health, running, image_id = 0, False, None
    while True:
        c = _inspect("container", CONTAINER)
        running = bool(c and (c.get("State") or {}).get("Running"))
        image_id = c.get("Image") if c else None
        health = _health() if running else 0
        if running and health in (200, 503):
            break
        if time.monotonic() >= deadline:
            break
        time.sleep(2)
    return {"ok": running and health in (200, 503), "running": running, "health": health, "image_id": image_id}


def agent_smoke(args) -> dict:
    out = CONTROLS / f"smoke-{args.revision[:9]}-{int(time.time())}.json"
    cmd = ["python3", str(SMOKE_PATH), "--container", CONTAINER, "--port", str(LOOPBACK_PORT),
           "--revision", args.revision, "--out", str(out)]
    if args.expect_draining:
        cmd.append("--expect-draining")
    proc = subprocess.run(cmd, capture_output=True, text=True, timeout=90)
    try:
        report = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return {"passed": False, "failed": ["smoke report unreadable"], "report": str(out)}
    failed = [case["case"] for case in report.get("cases", []) if case.get("passed") is not True]
    return {"passed": proc.returncode == 0 and not failed, "failed": failed, "report": str(out)}


def agent_release(args) -> dict:
    at = int(time.time())  # host clock, so the traffic window ignores operator clock skew
    _docker("kill", "-s", "USR2", CONTAINER)
    deadline = time.monotonic() + args.wait
    while True:
        health = _health()
        if health == 200 or time.monotonic() >= deadline:
            return {"health": health, "at": at}
        time.sleep(1)


_ACCESS_RE = re.compile(r'"component"\s*:\s*"http\.access"')
_PATH_RE = re.compile(r'"path"\s*:\s*"([^"]*)"')


def count_traffic(log_text: str) -> int:
    """Count access-log lines for non-/health* paths.

    Works for both zap encoders: JSON lines and the default console encoder,
    which appends the fields as a JSON object after the message.
    """
    seen = 0
    for line in log_text.splitlines():
        if not _ACCESS_RE.search(line):
            continue
        m = _PATH_RE.search(line)
        if m and not m.group(1).startswith("/health"):
            seen += 1
    return seen


def agent_traffic(args) -> dict:
    deadline = time.monotonic() + args.window
    while True:
        proc = _docker("logs", "--since", str(args.since), CONTAINER, check=False, timeout=30)
        count = count_traffic(proc.stdout + "\n" + proc.stderr)
        if count > 0 or time.monotonic() >= deadline:
            return {"seen": count > 0, "count": count}
        time.sleep(5)


def lease_op(action: str, holder: str, ttl: int, take_over: bool, path: Path = LEASE,
             now: Callable[[], _dt.datetime] = utcnow) -> dict:
    path.parent.mkdir(parents=True, exist_ok=True)
    lock = path.with_name(path.name + ".lock")
    with open(lock, "a") as lk:
        fcntl.flock(lk, fcntl.LOCK_EX)
        try:
            current = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError):
            current = None
        t = now()
        expired = bool(current) and parse_iso(current["expires_at"]) <= t
        if action == "read":
            return {"ok": True, "lease": current, "expired": expired}
        if action == "acquire":
            previous = None
            if current and current.get("holder") != holder:
                if not expired:
                    return {"ok": False, "reason": "held", "lease": current}
                if not take_over:
                    return {"ok": False, "reason": "expired-needs-take-over", "lease": current}
                previous = current
            lease = {"holder": holder, "started_at": iso(t), "expires_at": iso(t + _dt.timedelta(seconds=ttl))}
            if current and current.get("holder") == holder:
                lease["started_at"] = current["started_at"]
            _write_lease(path, lease)
            return {"ok": True, "lease": lease, "previous": previous}
        if not current or current.get("holder") != holder:
            return {"ok": action == "release" and not current, "reason": "not-holder", "lease": current}
        if action == "renew":
            current["expires_at"] = iso(t + _dt.timedelta(seconds=ttl))
            _write_lease(path, current)
            return {"ok": True, "lease": current}
        path.unlink()
        return {"ok": True, "lease": None}


def _write_lease(path: Path, lease: dict) -> None:
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(lease, sort_keys=True) + "\n")
    os.replace(tmp, path)


def agent_lease(args) -> dict:
    return lease_op(args.action, args.holder or "", args.ttl, args.take_over)


def build_agent_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="rolling.py agent")
    sub = parser.add_subparsers(dest="op", required=True)
    p = sub.add_parser("probe")
    p.add_argument("--tag")
    p.add_argument("--revision")
    sub.add_parser("vitals")
    p = sub.add_parser("signal")
    p.add_argument("--signal", choices=("USR1", "USR2"), required=True)
    p = sub.add_parser("drain")
    p.add_argument("--settle", type=int, default=DRAIN_SETTLE)
    p.add_argument("--max-wait", type=int, default=DRAIN_MAX)
    p.add_argument("--interval", type=int, default=DRAIN_INTERVAL)
    for name in ("repin", "restore"):
        p = sub.add_parser(name)
        p.add_argument("--tag", required=True)
        p.add_argument("--revision", required=True)
        if name == "restore":
            p.add_argument("--expect-ref", required=True)
    p = sub.add_parser("marker")
    p.add_argument("--remove", action="store_true")
    p = sub.add_parser("up")
    p.add_argument("--project", required=True)
    p.add_argument("--wait", type=int, default=120)
    p = sub.add_parser("smoke")
    p.add_argument("--revision", required=True)
    p.add_argument("--expect-draining", action="store_true")
    p = sub.add_parser("release")
    p.add_argument("--wait", type=int, default=30)
    p = sub.add_parser("traffic")
    p.add_argument("--since", type=int, required=True)
    p.add_argument("--window", type=int, default=TRAFFIC_WINDOW)
    p = sub.add_parser("lease")
    p.add_argument("action", choices=("read", "acquire", "renew", "release"))
    p.add_argument("--holder")
    p.add_argument("--ttl", type=int, default=LEASE_TTL)
    p.add_argument("--take-over", action="store_true")
    return parser


AGENT_OPS = {"probe": agent_probe, "vitals": agent_vitals, "signal": agent_signal, "drain": agent_drain,
             "repin": agent_repin, "restore": agent_restore, "marker": agent_marker, "up": agent_up,
             "smoke": agent_smoke, "release": agent_release, "traffic": agent_traffic, "lease": agent_lease}


def agent_main(argv: list[str]) -> int:
    args = build_agent_parser().parse_args(argv)
    for name in ("tag", "revision"):
        if getattr(args, name, None):
            (normalize_tag if name == "tag" else require_revision)(getattr(args, name))
    try:
        return _emit(AGENT_OPS[args.op](args))
    except RollingError as exc:
        sys.stderr.write(f"rolling agent {args.op}: {exc}\n")
        return 1


# --------------------------------------------------------------------------
# operator side
# --------------------------------------------------------------------------

class Executor(Protocol):
    def run(self, node: Any, script: str, *, timeout: int) -> Any: ...
    def put_file(self, node: Any, local: str, remote: str, *, mode: str = "600") -> Any: ...


class RemoteExecExecutor:
    """Executor backed by deploy/domestic/remote_exec.py (imported lazily)."""

    def __init__(self, via: str = "tat") -> None:
        try:
            from deploy.domestic import remote_exec  # type: ignore
        except ImportError:  # run as a script from deploy/domestic/
            import remote_exec  # type: ignore
        self.mod, self.via = remote_exec, via

    def run(self, node, script, *, timeout):
        try:
            return self.mod.run(node, script, timeout=timeout, via=self.via)
        except self.mod.RemoteExecError as exc:
            raise RollingError(f"{node.name}: remote exec refused: {exc}") from None

    def put_file(self, node, local, remote, *, mode="600"):
        try:
            return self.mod.put_file(node, local, remote, mode=mode, via=self.via)
        except self.mod.RemoteExecError as exc:
            raise RollingError(f"{node.name}: put_file refused: {exc}") from None


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class Remote:
    """Builds the one-line agent invocation and parses its JSON result."""

    def __init__(self, executor: Executor, agent_src: Path, smoke_src: Path) -> None:
        self.executor = executor
        self.agent_src, self.smoke_src = agent_src, smoke_src
        self.agent_sha, self.smoke_sha = _sha256(agent_src), _sha256(smoke_src)

    def script(self, op: str, *args: str) -> str:
        q = shlex.quote
        argv = ["python3", str(AGENT_PATH), "agent", op, *args]
        return "\n".join([
            "set -euo pipefail",
            f"printf '%s  %s\\n' {q(self.agent_sha)} {q(str(AGENT_PATH))} {q(self.smoke_sha)} {q(str(SMOKE_PATH))}"
            " | sha256sum -c --quiet -",
            " ".join(q(a) for a in argv),
            "",
        ])

    def call(self, node, op: str, *args: str, timeout: int = 120) -> dict:
        res = self.executor.run(node, self.script(op, *args), timeout=timeout)
        if res.status != "Success":
            raise RollingError(f"{node.name}: agent {op} {res.status} (exit {res.exit_code}); "
                               f"stderr: {(res.stderr_tail or '').strip()[-400:]}")
        for line in reversed((res.stdout_tail or "").splitlines()):
            if line.startswith(RESULT_PREFIX):
                return json.loads(line[len(RESULT_PREFIX):])
        raise RollingError(f"{node.name}: agent {op} returned no result")

    def install(self, node) -> None:
        res = self.executor.run(node, f"set -euo pipefail\ninstall -d -m 0700 {shlex.quote(str(CONTROLS))}\n",
                                timeout=60)
        if res.status != "Success":
            raise RollingError(f"{node.name}: cannot create {CONTROLS} ({res.status})")
        # remote_exec.put_file caps payloads at 48KB; ship gzip and unpack on the
        # host. Every agent call re-checks the unpacked sha256 anyway.
        with tempfile.TemporaryDirectory() as tmp:
            for src, dst in ((self.agent_src, AGENT_PATH), (self.smoke_src, SMOKE_PATH)):
                packed = Path(tmp) / (src.name + ".gz")
                packed.write_bytes(gzip.compress(src.read_bytes(), mtime=0))
                remote_gz = str(dst) + ".gz"
                res = self.executor.put_file(node, str(packed), remote_gz, mode="600")
                if res.status != "Success":
                    raise RollingError(f"{node.name}: put_file {remote_gz} {res.status}")
                q = shlex.quote
                unpack = "\n".join([
                    "set -euo pipefail",
                    f"gzip -dc {q(remote_gz)} > {q(str(dst) + '.tmp')}",
                    f"chmod 600 {q(str(dst) + '.tmp')}",
                    f"mv -f {q(str(dst) + '.tmp')} {q(str(dst))}",
                    f"rm -f {q(remote_gz)}",
                    "",
                ])
                res = self.executor.run(node, unpack, timeout=60)
                if res.status != "Success":
                    raise RollingError(f"{node.name}: unpack {dst} {res.status}")


# --- checks (pure, unit-tested) -------------------------------------------

def migration_hint(node) -> str:
    step = MIGRATION_STEP.get(node.name, "M2/M4")
    return f"complete migration step {step} first (design §迁移步骤)"


def layout_errors(node, facts: dict) -> list[str]:
    errs = []
    hint = migration_hint(node)
    # Only a RUNNING blue/green or proxy container means migration is unfinished.
    # Stopped containers are rollback material: compose up never touches them, and
    # they hold neither the port nor the container name. app-a keeps tokenkey-blue
    # and tokenkey-green stopped after M4 for exactly that reason (2026-11-15).
    # tokenkey-caddy is app-a's direct-site fallback. After M4 the cloud
    # firewall closes 80/443, so it serves nothing reachable from outside, and
    # rolling only runs `up -d --no-deps tokenkey`, which never touches it.
    # Keeping it running is the one-step rollback for the direct sites.
    blocking = tuple(n for n in FORBIDDEN_CONTAINERS if n != "tokenkey-caddy")
    stale = [n for n in blocking if (facts.get("containers") or {}).get(n) == "running"]
    if stale:
        errs.append(f"blue/green or proxy containers still running: {', '.join(stale)}; {hint}")
    c = facts.get("container")
    if c is None:
        errs.append(f"no '{CONTAINER}' container; {hint}")
    else:
        if c.get("socat"):
            errs.append(f"'{CONTAINER}' is a socat forwarder, not the gateway; {hint}")
        want = {f"{node.private_ip}:8080", f"127.0.0.1:{LOOPBACK_PORT}"}
        if c.get("running") and not want <= set(c.get("ports") or []):
            errs.append(f"'{CONTAINER}' must publish {sorted(want)}, has {c.get('ports')}; {hint}")
        if not c.get("data_mount"):
            errs.append(f"'{CONTAINER}' must bind {DATA_MOUNT[0]} -> {DATA_MOUNT[1]} rw")
        if not PROJECT_RE.match(c.get("project") or ""):
            errs.append(f"'{CONTAINER}' is not a compose-managed container; {hint}")
    missing = [n for n, ok in (facts.get("compose_files") or {}).items() if not ok]
    if missing:
        errs.append(f"missing compose files under {ROOT}: {', '.join(missing)}; {hint}")
    env = facts.get("env") or {}
    if not env.get("exists"):
        errs.append(f"{ROOT}/.env missing")
    else:
        if env.get("mode") != "600":
            errs.append(f"{ROOT}/.env mode is {env.get('mode')}, want 600")
        pins = env.get("pins") or []
        if len(pins) != 1:
            errs.append(f".env must contain exactly one TOKENKEY_IMAGE line (found {len(pins)})")
        elif not pins[0].startswith(LOCAL_REPO + ":"):
            errs.append(f".env TOKENKEY_IMAGE must pin a local {LOCAL_REPO} tag")
    return errs


def target_errors(facts: dict, tag: str, revision: str) -> list[str]:
    t = facts.get("target")
    ltag = local_tag(tag, revision)
    if not t:
        return [f"target image {ltag} not prewarmed; run distribute-cn-image / deploy_via_ssm.sh pull first"]
    errs = []
    if not t.get("receipt_image_id"):
        errs.append(f"no warm receipt {IMAGE_RECEIPTS}/{tag}-{revision}.json")
    elif t["receipt_image_id"] != t["image_id"] or t.get("receipt_local_tag") != ltag:
        errs.append(f"{ltag} image id {short_id(t['image_id'])} differs from warm receipt "
                    f"{short_id(t['receipt_image_id'])}")
    if t.get("architecture") != "amd64":
        errs.append(f"{ltag} architecture {t.get('architecture')}, want amd64")
    if t.get("revision") != revision:
        errs.append(f"{ltag} revision label {t.get('revision')!r} != {revision}")
    if t.get("version") != tag:
        errs.append(f"{ltag} version label {t.get('version')!r} != {tag}")
    if not version_at_least(t.get("version") or ""):
        errs.append(f"{ltag} version {t.get('version')!r} is below MIN_ROLLING_VERSION {MIN_ROLLING_VERSION} "
                    "(no start-draining marker / SIGUSR2); use the blue/green path")
    return errs


def credential_errors(facts: dict) -> list[str]:
    if facts.get("compose_error"):
        return [f"credential parity not verified: {facts['compose_error']}"]
    drift = facts.get("credential_drift")
    if drift is None:
        return ["credential parity not verified (container or compose files missing)"]
    if drift:
        return ["compose-rendered credentials differ from the live container: " + ", ".join(drift)
                + f"; fix {ROOT}/.env first (values not shown)"]
    return []


def capacity_errors(node, vitals: dict) -> list[str]:
    if vitals.get("health") != 200:
        return [f"{node.name} /health={vitals.get('health')}; refusing to drain another node "
                "(the last healthy app node must stay in rotation)"]
    errs = []
    cpu, mem, limit = vitals.get("cpu_percent"), vitals.get("mem_percent"), vitals.get("memory_limit")
    if cpu is None or cpu >= CPU_LIMIT:
        errs.append(f"{node.name} CPU {cpu}% >= {CPU_LIMIT}% (or unknown)")
    if not limit:
        errs.append(f"{node.name} container has no memory limit; cannot apply the {MEM_LIMIT}% rule")
    elif mem is None or mem >= MEM_LIMIT:
        errs.append(f"{node.name} memory {mem}% of limit >= {MEM_LIMIT}% (or unknown)")
    return errs


def classify(facts: dict, target_id: str | None) -> str:
    c = facts.get("container") or {}
    image_id = c.get("image_id")
    health = facts.get("health")
    if not c.get("running"):
        return "stuck"
    if health == 200 and image_id == target_id and target_id:
        return "skip"
    if health == 503:
        return "resume-smoke" if image_id == target_id and target_id else "resume-swap"
    if health == 200:
        return "full"
    return "stuck"


STEPS = {
    "skip": [],
    "full": ["drain", "repin", "marker", "up", "smoke", "release", "traffic"],
    "resume-swap": ["drain", "repin", "marker", "up", "smoke", "release", "traffic"],
    "resume-smoke": ["smoke", "release", "traffic"],
    "stuck": [],
}


def node_errors(node, facts: dict, mode: str, tag: str, revision: str) -> list[str]:
    errs = layout_errors(node, facts)
    if errs or mode == "skip":
        return errs
    if mode == "stuck":
        return [f"{node.name} is neither serving nor draining (running={facts.get('running')}, "
                f"/health={facts.get('health')}); recover it by hand before rolling"]
    errs += target_errors(facts, tag, revision)
    errs += credential_errors(facts)
    c, target = facts["container"], facts.get("target") or {}
    old_version = ((facts.get("backup") or {}) if mode == "resume-smoke" else (facts.get("current_image") or {})).get("version")
    if not version_at_least(old_version or ""):
        # Abort and rollback both send SIGUSR2 to the old image; a Go binary
        # without a SIGUSR2 handler terminates on it (default action).
        errs.append(f"{node.name} rollback image version {old_version!r} is below {MIN_ROLLING_VERSION} "
                    "(no SIGUSR2 handler: drain abort would kill it); finish M1 first")
    if mode == "full" and facts.get("pin_image_id") != c["image_id"]:
        errs.append(f"{node.name} .env TOKENKEY_IMAGE resolves to {short_id(facts.get('pin_image_id'))} but the "
                    f"running image is {short_id(c['image_id'])}; rollback would not restore it")
    if mode == "resume-swap" and facts.get("pin_image_id") not in (c["image_id"], target.get("image_id")):
        errs.append(f"{node.name} .env pin matches neither the running nor the target image")
    if mode == "resume-smoke" and not (facts.get("backup") or {}).get("image_id"):
        errs.append(f"{node.name} runs the target drained but has no usable {backup_name(tag, revision)}; "
                    "rollback target unknown")
    return errs


# --- receipts ----------------------------------------------------------------

@dataclass
class NodeReport:
    name: str
    mode: str
    before_image_id: str | None
    after_image_id: str | None = None
    outcome: str = "pending"
    steps: list = field(default_factory=list)
    warnings: list = field(default_factory=list)

    def step(self, name: str, ok: bool, started: float, **detail) -> None:
        self.steps.append({"step": name, "ok": ok, "seconds": round(time.monotonic() - started, 2), **detail})


@dataclass
class Run:
    tag: str
    revision: str
    command: str
    started_at: str = field(default_factory=lambda: iso(utcnow()))
    finished_at: str | None = None
    outcome: str = "pending"
    holder: str | None = None
    target_local_tag: str | None = None
    nodes: list = field(default_factory=list)
    errors: list = field(default_factory=list)
    manual_steps: list = field(default_factory=list)
    lease_previous: dict | None = None

    def to_json(self) -> dict:
        return {
            "schema_version": 1, "command": self.command, "tag": self.tag, "revision": self.revision,
            "target_local_tag": self.target_local_tag, "started_at": self.started_at,
            "finished_at": self.finished_at, "outcome": self.outcome, "holder": self.holder,
            "lease_previous_holder": (self.lease_previous or {}).get("holder"),
            "errors": self.errors, "manual_steps": self.manual_steps,
            "nodes": [n.__dict__ for n in self.nodes],
        }


def write_receipt(run: Run, out: Path | None) -> None:
    if out is None:
        return
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(run.to_json(), ensure_ascii=False, indent=2) + "\n")
    out.chmod(0o600)


def log(msg: str) -> None:
    print(f"[rolling] {msg}", flush=True)


# --- orchestration -----------------------------------------------------------

class Stop(Exception):
    """Stop the rollout after the current node. exit_code distinguishes warn vs fail."""

    def __init__(self, message: str, exit_code: int = EXIT_FAIL, manual: list[str] | None = None):
        super().__init__(message)
        self.exit_code, self.manual = exit_code, manual or []


class Roller:
    def __init__(self, remote: Remote, nodes: list, entry, tag: str, revision: str,
                 *, holder: str = "", take_over: bool = False):
        self.remote, self.nodes, self.entry = remote, nodes, entry
        self.tag, self.revision = tag, revision
        self.holder, self.take_over = holder, take_over
        self.ltag = local_tag(tag, revision)

    # remote shorthands
    def _call(self, node, op, *args, timeout=120):
        return self.remote.call(node, op, *args, timeout=timeout)

    def probe(self, node) -> dict:
        return self._call(node, "probe", "--tag", self.tag, "--revision", self.revision)

    def assess(self) -> tuple[dict, dict, list[str]]:
        """Probe every app node; return facts, modes and blocking errors (read-only)."""
        if len(self.nodes) < 2:
            raise RollingError(f"rolling needs at least two app nodes, manifest has {len(self.nodes)}")
        facts, modes, errs = {}, {}, []
        for node in self.nodes:
            facts[node.name] = f = self.probe(node)
            modes[node.name] = classify(f, (f.get("target") or {}).get("image_id"))
            errs += [f"{node.name}: {e}" for e in node_errors(node, f, modes[node.name], self.tag, self.revision)]
        return facts, modes, errs

    def plan_lines(self, facts: dict, modes: dict) -> list[str]:
        lines = []
        for node in self.nodes:
            f = facts[node.name]
            cur = short_id((f.get("container") or {}).get("image_id"))
            tgt = short_id((f.get("target") or {}).get("image_id"))
            steps = " -> ".join(STEPS[modes[node.name]]) or "(none)"
            lines.append(f"{node.name} ({node.private_ip}): current={cur} target={tgt} health={f.get('health')} "
                         f"mode={modes[node.name]} steps: {steps}")
        return lines

    # lease ------------------------------------------------------------------
    def lease(self, action: str) -> dict:
        args = ["lease", action, "--holder", self.holder, "--ttl", str(LEASE_TTL)]
        if self.take_over and action == "acquire":
            args.append("--take-over")
        return self._call(self.entry, *args, timeout=60)

    def acquire(self, run: Run) -> None:
        r = self.lease("acquire")
        if not r.get("ok"):
            held = (r.get("lease") or {})
            who = f"{held.get('holder')} (started {held.get('started_at')}, expires {held.get('expires_at')})"
            if r.get("reason") == "expired-needs-take-over":
                raise RollingError(f"lease {LEASE} expired but still held by {who}; rerun with --take-over")
            raise RollingError(f"lease {LEASE} held by {who}")
        if r.get("previous"):
            run.lease_previous = r["previous"]
            log(f"took over expired lease from {r['previous'].get('holder')} "
                f"(started {r['previous'].get('started_at')}, expired {r['previous'].get('expires_at')})")

    # node state machine -----------------------------------------------------
    def roll(self, run: Run) -> int:
        for node in [*self.nodes, self.entry]:
            self.remote.install(node)
        self.acquire(run)
        exit_code = EXIT_OK
        try:
            facts, modes, errs = self.assess()
            if errs:
                run.errors += errs
                raise RollingError("preflight refused:\n  " + "\n  ".join(errs))
            for line in self.plan_lines(facts, modes):
                log("plan " + line)
            # Drained (resume) nodes go first: they are already out of rotation, and a
            # full node may only be drained once every other node serves again.
            order = sorted(self.nodes, key=lambda n: 0 if modes[n.name].startswith("resume") else 1)
            for node in order:
                f = self.probe(node)  # fresh: earlier nodes changed the fleet
                mode = classify(f, (f.get("target") or {}).get("image_id"))
                rep = NodeReport(node.name, mode, (f.get("container") or {}).get("image_id"))
                run.nodes.append(rep)
                errs = node_errors(node, f, mode, self.tag, self.revision)
                if errs:
                    rep.outcome = "refused"
                    run.errors += [f"{node.name}: {e}" for e in errs]
                    raise RollingError(f"{node.name} refused: " + "; ".join(errs))
                try:
                    self.roll_node(node, f, mode, rep)
                except Stop as stop:
                    run.errors.append(f"{node.name}: {stop}")
                    run.manual_steps += stop.manual
                    exit_code = stop.exit_code
                    break
                renew = self.lease("renew")
                if not renew.get("ok"):
                    raise RollingError(f"lease lost after {node.name}: {renew.get('reason')}")
        finally:
            try:
                self.lease("release")
            except RollingError as exc:
                log(f"lease release failed: {exc}")
        return exit_code

    def roll_node(self, node, f: dict, mode: str, rep: NodeReport) -> None:
        target_id = f["target"]["image_id"]
        if mode == "skip":
            rep.outcome, rep.after_image_id = "skipped", target_id
            log(f"{node.name}: already on {short_id(target_id)} and serving; skip")
            return
        if mode == "full":
            for other in self.nodes:
                if other is node:
                    continue
                errs = capacity_errors(other, self._call(other, "vitals", timeout=60))
                if errs:
                    rep.outcome = "refused"
                    raise RollingError(f"{node.name}: cannot drain: " + "; ".join(errs))
        else:
            log(f"{node.name}: draining at /health=503 on {short_id(rep.before_image_id)}; resuming ({mode})")

        c = f["container"]
        if mode == "resume-smoke":
            b = f["backup"]
            old = {"ref": b["pin"], "id": b["image_id"], "revision": b["revision"]}
        else:
            old = {"ref": f["env"]["pins"][0] if f.get("pin_image_id") == c["image_id"] else None,
                   "id": c["image_id"], "revision": (f.get("current_image") or {}).get("revision")}
        project = c["project"]

        if mode in ("full", "resume-swap"):
            t = time.monotonic()
            d = self._call(node, "drain", timeout=DRAIN_SETTLE + DRAIN_MAX + 120)
            rep.step("drain", d.get("drained") is True and d.get("health") == 503, t,
                     in_flight=d.get("in_flight"), health=d.get("health"), tries=d.get("tries"))
            if not (d.get("drained") and d.get("health") == 503):
                self.abort_drain(node, rep, d)
            try:
                t = time.monotonic()
                r = self._call(node, "repin", "--tag", self.tag, "--revision", self.revision)
                old["ref"] = old["ref"] or r.get("old_ref")
                rep.step("repin", True, t)
                t = time.monotonic()
                self._call(node, "marker")
                rep.step("marker", True, t)
                t = time.monotonic()
                up = self._call(node, "up", "--project", project, timeout=420)
                ok = up.get("ok") and up.get("image_id") == target_id and up.get("health") == 503
                rep.step("up", bool(ok), t, health=up.get("health"), image_id=up.get("image_id"))
                if up.get("health") == 200:
                    self._call(node, "signal", "--signal", "USR1")
                    raise _Failed(f"{self.ltag} served /health=200 right after start: the image ignores "
                                  "the start-draining marker; drained it again")
                if not ok:
                    raise _Failed(f"new container not up drained (running={up.get('running')}, "
                                  f"health={up.get('health')}, image={short_id(up.get('image_id'))})")
            except (RollingError, _Failed) as exc:
                self.rollback(node, rep, old, project, str(exc))
        try:
            t = time.monotonic()
            s = self._call(node, "smoke", "--revision", self.revision, "--expect-draining", timeout=180)
            rep.step("smoke", s.get("passed") is True, t, failed=s.get("failed"))
            if not s.get("passed"):
                raise _Failed("smoke failed: " + ", ".join(s.get("failed") or []))
            t = time.monotonic()
            rel = self._call(node, "release", timeout=120)
            since = int(rel.get("at") or 0)
            rep.step("release", rel.get("health") == 200, t, health=rel.get("health"))
            if rel.get("health") != 200:
                raise _Failed(f"/health={rel.get('health')} after SIGUSR2")
        except (RollingError, _Failed) as exc:
            self.rollback(node, rep, old, project, str(exc))
        rep.after_image_id = target_id
        rep.outcome = "rolled"
        t = time.monotonic()
        try:
            tr = self._call(node, "traffic", "--since", str(since), timeout=TRAFFIC_WINDOW + 120)
        except RollingError as exc:
            tr = {"seen": False, "count": None, "error": str(exc)}
        rep.step("traffic", tr.get("seen") is True, t, count=tr.get("count"))
        log(f"{node.name}: now on {short_id(target_id)} and serving")
        if not tr.get("seen"):
            rep.warnings.append(f"no non-/health request within {TRAFFIC_WINDOW}s after release")
            raise Stop(f"{node.name} is healthy on the new image but received no traffic within "
                       f"{TRAFFIC_WINDOW}s; rollout stopped (no rollback)", EXIT_WARN, [
                           f"check entry Caddy upstream for {node.private_ip}:8080 and its access log",
                           "rerun the same deploy command once traffic is confirmed; done nodes are skipped"])

    def abort_drain(self, node, rep: NodeReport, d: dict) -> None:
        log(f"{node.name}: drain did not finish (in_flight={d.get('in_flight')}); releasing old container")
        t = time.monotonic()
        rel = self._call(node, "release", timeout=120)
        rep.step("release-old", rel.get("health") == 200, t, health=rel.get("health"))
        rep.outcome = "drain-timeout"
        rep.after_image_id = rep.before_image_id
        manual = [f"inspect long requests on {node.name}: docker exec {CONTAINER} wget -qO- "
                  "http://localhost:8080/health/inflight",
                  "rerun the deploy in a quieter window; nothing was changed on this node"]
        if rel.get("health") != 200:
            manual.insert(0, f"{node.name} still answers /health={rel.get('health')}: "
                             f"send `docker kill -s USR2 {CONTAINER}` by hand")
        raise Stop(f"drain not complete within {DRAIN_MAX}s (in_flight={d.get('in_flight')}); "
                   "node left on the old image", EXIT_FAIL, manual)

    def rollback(self, node, rep: NodeReport, old: dict, project: str, reason: str) -> None:
        log(f"{node.name}: {reason}; rolling this node back to {short_id(old.get('id'))}")
        rep.warnings.append(reason)
        manual_base = [f"{node.name} is out of rotation; the other app nodes keep serving",
                       f"restore by hand: cp {ROOT}/{backup_name(self.tag, self.revision)} {ROOT}/.env "
                       f"(keep 0600); touch {MARKER}; docker compose -p {project} --project-directory {ROOT} "
                       + " ".join(f"-f {n}" for n in COMPOSE_FILES)
                       + f" up -d --no-deps {CONTAINER}; smoke; docker kill -s USR2 {CONTAINER}"]
        try:
            if not old.get("ref") or not old.get("id") or not REV_RE.match(old.get("revision") or ""):
                raise _Failed("old image reference or revision label unknown")
            t = time.monotonic()
            vit = self._call(node, "vitals", timeout=60)
            if vit.get("running"):
                d = self._call(node, "drain", timeout=DRAIN_SETTLE + DRAIN_MAX + 120)
                rep.step("rollback-drain", bool(d.get("drained")), t, in_flight=d.get("in_flight"))
                if not d.get("drained"):
                    raise _Failed(f"new container did not drain (in_flight={d.get('in_flight')}); "
                                  "not recreating under live requests")
            t = time.monotonic()
            self._call(node, "restore", "--tag", self.tag, "--revision", self.revision, "--expect-ref", old["ref"])
            rep.step("rollback-restore-env", True, t)
            t = time.monotonic()
            self._call(node, "marker")
            up = self._call(node, "up", "--project", project, timeout=420)
            ok = up.get("ok") and up.get("image_id") == old["id"]
            rep.step("rollback-up", bool(ok), t, health=up.get("health"), image_id=up.get("image_id"))
            if not ok:
                raise _Failed(f"old image not up (health={up.get('health')}, image={short_id(up.get('image_id'))})")
            drained = up.get("health") == 503
            if not drained:
                raise _Failed("old image served /health=200 before smoke (ignores the start-draining marker)")
            t = time.monotonic()
            smoke_args = ["--revision", old["revision"]] + (["--expect-draining"] if drained else [])
            s = self._call(node, "smoke", *smoke_args, timeout=180)
            rep.step("rollback-smoke", s.get("passed") is True, t, failed=s.get("failed"))
            if not s.get("passed"):
                raise _Failed("old image smoke failed: " + ", ".join(s.get("failed") or []))
            if drained:
                t = time.monotonic()
                rel = self._call(node, "release", timeout=120)
                rep.step("rollback-release", rel.get("health") == 200, t, health=rel.get("health"))
                if rel.get("health") != 200:
                    raise _Failed(f"/health={rel.get('health')} after SIGUSR2 on the old image")
        except (RollingError, _Failed) as exc:
            rep.outcome = "rollback-failed"
            raise Stop(f"{reason}; ROLLBACK FAILED: {exc}", EXIT_FAIL, manual_base) from None
        rep.outcome = "rolled-back"
        rep.after_image_id = old["id"]
        raise Stop(f"{reason}; node rolled back to {short_id(old['id'])} and serving; rollout stopped",
                   EXIT_FAIL, [f"{node.name} is back on the old image; finished nodes stay on {self.ltag}",
                               f"read the smoke report under {CONTROLS} on {node.name}",
                               "after a fix, rerun deploy with the same or a new tag; done nodes are skipped"])


class _Failed(Exception):
    pass


# --- commands ---------------------------------------------------------------

def default_holder() -> str:
    who = os.environ.get("GITHUB_ACTOR") or getpass.getuser()
    where = f"gha-run-{os.environ['GITHUB_RUN_ID']}" if os.environ.get("GITHUB_RUN_ID") else socket.gethostname()
    return f"{who}@{where}#{uuid.uuid4().hex[:8]}"


def cmd_plan(roller: Roller, run: Run) -> int:
    for node in [*roller.nodes]:
        roller.remote.install(node)
    facts, modes, errs = roller.assess()
    for line in roller.plan_lines(facts, modes):
        print(line)
    for node in roller.nodes:
        run.nodes.append(NodeReport(node.name, modes[node.name],
                                    (facts[node.name].get("container") or {}).get("image_id"),
                                    outcome="planned"))
    if errs:
        run.errors += errs
        print("BLOCKED:\n  " + "\n  ".join(errs))
        return EXIT_FAIL
    print("plan ok")
    return EXIT_OK


def cmd_status(remote: Remote, nodes: list, entry) -> int:
    for node in [*nodes, entry]:
        remote.install(node)
    for node in nodes:
        f = remote.call(node, "probe")
        c = f.get("container") or {}
        print(f"{node.name} ({node.private_ip}): image={short_id(c.get('image_id'))} "
              f"version={(f.get('current_image') or {}).get('version')} health={f.get('health')} "
              f"inflight={f.get('inflight')} marker={f.get('marker')}")
        for e in layout_errors(node, f):
            print(f"  layout: {e}")
    lease = remote.call(entry, "lease", "read", "--holder", "status")
    print(f"lease: {lease.get('lease')} expired={lease.get('expired')}")
    return EXIT_OK


def conclude(run: Run, code: int) -> None:
    run.finished_at = iso(utcnow())
    run.outcome = {EXIT_OK: "success", EXIT_WARN: "stopped-warning"}.get(code, "failed")
    print(f"\n=== rolling {run.command} {run.tag} ({run.revision[:9]}): {run.outcome.upper()} ===")
    for n in run.nodes:
        print(f"  {n.name}: {n.outcome} {short_id(n.before_image_id)} -> {short_id(n.after_image_id)}"
              + (f" ({'; '.join(n.warnings)})" if n.warnings else ""))
    for e in run.errors:
        print(f"  error: {e}")
    if run.manual_steps:
        print("  manual steps:")
        for i, s in enumerate(run.manual_steps, 1):
            print(f"    {i}. {s}")


def build_parser() -> argparse.ArgumentParser:
    here = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description="cn-prod rolling deploy (design-cn-prod-rolling-deploy.md)")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("plan", "deploy", "status"):
        p = sub.add_parser(name)
        p.add_argument("--manifest", type=Path, default=here / "prod-target.json")
        p.add_argument("--via", choices=("tat", "ssm"), default="tat")
        if name == "status":
            continue
        p.add_argument("--tag", required=True)
        p.add_argument("--revision", required=True)
        p.add_argument("--receipt-out", type=Path, required=(name == "deploy"))
        if name == "deploy":
            p.add_argument("--confirm", default="")
            p.add_argument("--take-over", action="store_true")
    return parser


def main(argv: list[str] | None = None, *, executor: Executor | None = None,
         nodes: list | None = None, entry=None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv[:1] == ["agent"]:
        return agent_main(argv[1:])
    if not argv or argv[0] not in ("plan", "deploy", "status", "-h", "--help"):
        argv.insert(0, "plan")
    args = build_parser().parse_args(argv)
    try:
        try:
            from deploy.domestic import remote_exec  # type: ignore
        except ImportError:
            import remote_exec  # type: ignore
        raw_manifest = json.loads(Path(args.manifest).read_text())
        if args.command in ("plan", "deploy"):
            apply_layout(require_rolling_layout(raw_manifest))
        else:
            apply_layout(layout_for_manifest(raw_manifest))
        if nodes is None or entry is None:
            nodes = remote_exec.app_nodes(args.manifest)
            entry = remote_exec.load_nodes(args.manifest)["entry"]
        executor = executor or RemoteExecExecutor(args.via)
        here = Path(__file__).resolve().parent
        remote = Remote(executor, here / "rolling.py", here / "candidate_smoke.py")
        if args.command == "status":
            return cmd_status(remote, nodes, entry)
        tag, revision = normalize_tag(args.tag), require_revision(args.revision)
        if args.command == "deploy" and args.confirm != f"rolling-cn-prod-{tag}":
            raise RollingError(f"deploy needs --confirm rolling-cn-prod-{tag}")
    except RollingError as exc:
        print(f"refused: {exc}", file=sys.stderr)
        return EXIT_USAGE if "--confirm" in str(exc) or "must be" in str(exc) else EXIT_FAIL
    run = Run(tag, revision, args.command, target_local_tag=local_tag(tag, revision))
    roller = Roller(remote, nodes, entry, tag, revision,
                    holder=default_holder(), take_over=getattr(args, "take_over", False))
    run.holder = roller.holder if args.command == "deploy" else None
    try:
        code = cmd_plan(roller, run) if args.command == "plan" else roller.roll(run)
    except RollingError as exc:
        if str(exc) not in run.errors and not str(exc).startswith("preflight refused"):
            run.errors.append(str(exc))
        code = EXIT_FAIL
    conclude(run, code)
    write_receipt(run, args.receipt_out)
    return code


if __name__ == "__main__":
    raise SystemExit(main())
