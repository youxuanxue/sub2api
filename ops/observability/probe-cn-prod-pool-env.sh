#!/usr/bin/env bash
# Read cn-prod app pool env. Opt-in write: APPLY=yes-tune-cn-prod-pools
# Updates compose.override.yml and the active bluegreen service compose env,
# then force-recreates that service via docker compose (no secrets on argv).
#
# SSOT for target values: deploy/domestic/compose.override.yml (repo).
# WANT_* below must stay identical; deploy/domestic/test_pool_env_probe.py guards drift.
set -euo pipefail

ROOT="${TOKENKEY_ROOT:-/opt/tokenkey}"
OVERRIDE="$ROOT/compose.override.yml"
CADDY_FILE="${CADDY_FILE:-/var/lib/tokenkey/caddy/Caddyfile}"
APPLY="${APPLY:-}"

export WANT_GOMEM="1GiB"
export WANT_DB_OPEN="40"
export WANT_DB_IDLE="10"
export WANT_REDIS_POOL="96"
export WANT_REDIS_IDLE="8"
export OVERRIDE CADDY_FILE APPLY

[[ -f "$OVERRIDE" ]] || { echo "FATAL: missing $OVERRIDE" >&2; exit 1; }
[[ -f "$CADDY_FILE" ]] || { echo "FATAL: missing $CADDY_FILE" >&2; exit 1; }

python3 - <<'PY'
import json, os, re, shutil, subprocess, sys, tempfile, time
from pathlib import Path

override = Path(os.environ["OVERRIDE"])
caddy = Path(os.environ["CADDY_FILE"]).read_text()
apply = os.environ.get("APPLY", "")
want = {
    "GOMEMLIMIT": os.environ["WANT_GOMEM"],
    "DATABASE_MAX_OPEN_CONNS": os.environ["WANT_DB_OPEN"],
    "DATABASE_MAX_IDLE_CONNS": os.environ["WANT_DB_IDLE"],
    "REDIS_POOL_SIZE": os.environ["WANT_REDIS_POOL"],
    "REDIS_MIN_IDLE_CONNS": os.environ["WANT_REDIS_IDLE"],
}

routes = re.findall(r"^\s*reverse_proxy\s+(tokenkey(?:-blue|-green)?):8080", caddy, re.M)
if len(routes) != 1:
    raise SystemExit(f"FATAL: expected one app upstream, got {routes!r}")
active = routes[0]
print(f"active_container={active}")
print(f"override={override}")

def container_pool_env(name: str) -> dict[str, str]:
    out = subprocess.check_output(
        ["docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", name],
        text=True,
    )
    got = {}
    for line in out.splitlines():
        if "=" not in line:
            continue
        k, v = line.split("=", 1)
        if k in want:
            got[k] = v
    return got

def print_pool(label: str, values: dict[str, str]) -> None:
    print(label)
    for k in want:
        print(f"  {k}={values.get(k, 'MISSING')}")

def patch_override_text(text: str) -> str:
    new = text
    for key, value in want.items():
        pattern = rf'(?m)^(\s*{key}:\s*")[^"]*(")'
        if not re.search(pattern, new):
            raise SystemExit(f"FATAL: key {key} not found in override")
        new = re.sub(pattern, rf"\g<1>{value}\g<2>", new, count=1)
    return new

def atomic_write(path: Path, content: str) -> None:
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", dir=str(path.parent))
    os.close(fd)
    tmp_path = Path(tmp)
    try:
        tmp_path.write_text(content)
        os.chmod(tmp_path, path.stat().st_mode & 0o777)
        tmp_path.replace(path)
    finally:
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)

def compose_up(project: str, workdir: str, compose_path: Path, service: str) -> None:
    subprocess.check_call([
        "docker", "compose",
        "-p", project,
        "--project-directory", workdir,
        "-f", str(compose_path),
        "up", "-d", "--force-recreate", "--no-deps", service,
    ])

def wait_healthy(name: str, seconds: int = 120) -> bool:
    deadline = time.time() + seconds
    while time.time() < deadline:
        st = subprocess.check_output(
            ["docker", "inspect", "--format",
             "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", name],
            text=True,
        ).strip()
        print(f"health={st}")
        if st == "healthy":
            return True
        if st in ("exited", "dead"):
            return False
        time.sleep(3)
    return False

text = override.read_text()
print("--- override before ---")
for key in want:
    m = re.search(rf'(?m)^\s*{key}:\s*"([^"]+)"', text)
    print(f"  {key}={m.group(1) if m else 'MISSING'}")

before = container_pool_env(active)
print_pool("--- container before ---", before)

new_override = patch_override_text(text)
already = before == want and new_override == text
if already:
    print("status=already_at_target")
    raise SystemExit(0)

if apply != "yes-tune-cn-prod-pools":
    print("mode=dry-run")
    print("status=would_change")
    raise SystemExit(0)

override_backup = None
if new_override != text:
    override_backup = override.with_suffix(override.suffix + ".pre-pooltune")
    shutil.copy2(override, override_backup)
    atomic_write(override, new_override)
    print("override_updated=true")
else:
    print("override_updated=false")

labels = json.loads(subprocess.check_output(
    ["docker", "inspect", "--format", "{{json .Config.Labels}}", active], text=True
))
project = labels.get("com.docker.compose.project")
workdir = labels.get("com.docker.compose.project.working_dir")
config_files = labels.get("com.docker.compose.project.config_files")
service = labels.get("com.docker.compose.service") or active
if not project or not workdir or not config_files:
    raise SystemExit("FATAL: active container missing compose labels; refusing manual recreate")

compose_path = Path(config_files.split(",")[0].strip())
if not compose_path.is_file():
    raise SystemExit(f"FATAL: missing compose file {compose_path}")

compose_backup = compose_path.with_suffix(compose_path.suffix + ".pre-pooltune")
shutil.copy2(compose_path, compose_backup)
print(f"compose_backup={compose_backup}")

compose = json.loads(compose_path.read_text())
svc = (compose.get("services") or {}).get(service)
if not isinstance(svc, dict):
    raise SystemExit(f"FATAL: service {service} not in {compose_path}")
env = svc.get("environment")
if not isinstance(env, dict):
    raise SystemExit("FATAL: service environment is not a mapping")
for key, value in want.items():
    env[key] = value
atomic_write(compose_path, json.dumps(compose, indent=2) + "\n")
print(f"compose_updated={compose_path}")

print(f"recreate_service={service} project={project}")
# Do not print full command env; compose reads file.
compose_up(project, workdir, compose_path, service)

if not wait_healthy(active):
    print("FATAL: service unhealthy after recreate; rolling back", file=sys.stderr)
    shutil.copy2(compose_backup, compose_path)
    if override_backup is not None and override_backup.is_file():
        shutil.copy2(override_backup, override)
    try:
        compose_up(project, workdir, compose_path, service)
        wait_healthy(active)
    except Exception as exc:  # noqa: BLE001 — best-effort restore then still fail
        print(f"FATAL: rollback recreate failed: {exc}", file=sys.stderr)
    raise SystemExit(1)

after = container_pool_env(active)
print_pool("--- container after ---", after)
if after != want:
    print("FATAL: env mismatch after apply; rolling back", file=sys.stderr)
    shutil.copy2(compose_backup, compose_path)
    if override_backup is not None and override_backup.is_file():
        shutil.copy2(override_backup, override)
    compose_up(project, workdir, compose_path, service)
    wait_healthy(active)
    raise SystemExit(1)

print("apply_ok=true")
print(f"compose_backup_kept={compose_backup}")
if override_backup is not None:
    print(f"override_backup_kept={override_backup}")
PY
