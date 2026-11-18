#!/usr/bin/env bash
# Lightweight long-window sampler for cn-prod formal TPM burst runs.
# Roles:
#   app   — host load + tokenkey docker CPU/mem
#   data  — host load + PG total/active + PG docker CPU/mem
#   entry — host load + Caddy docker CPU/mem + established TCP count
#
# Env:
#   ROLE         app|data|entry (required)
#   OUT          jsonl path (default /tmp/tpm-mon/lite.jsonl)
#   INTERVAL_S   sample period seconds (default 6)
#   DURATION_S   optional; if set, exit after this many seconds (else run until killed)
set -euo pipefail

ROLE="${ROLE:-}"
case "$ROLE" in
  app|data|entry) ;;
  *) echo '{"error":"ROLE must be app|data|entry"}' >&2; exit 2 ;;
esac

OUT="${OUT:-/tmp/tpm-mon/lite.jsonl}"
INTERVAL_S="${INTERVAL_S:-6}"
DURATION_S="${DURATION_S:-}"
case "$INTERVAL_S" in ''|*[!0-9]*) echo '{"error":"bad INTERVAL_S"}' >&2; exit 2 ;; esac
if [ -n "$DURATION_S" ]; then
  case "$DURATION_S" in ''|*[!0-9]*) echo '{"error":"bad DURATION_S"}' >&2; exit 2 ;; esac
fi
if [ "$INTERVAL_S" -lt 1 ]; then
  echo '{"error":"INTERVAL_S>=1 required"}' >&2
  exit 2
fi

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo '{"error":"container resolver not found"}' >&2; exit 1; }

TK_APP=""
TK_PG=""
TK_PG_SERVER=""
case "$ROLE" in
  app)
    TK_APP="$(tk_resolve_app_container "${APP_CONTAINER:-auto}")" || {
      echo '{"error":"app container unresolved"}' >&2
      exit 1
    }
    ;;
  data)
    # psql may use server or dataplane client; docker stats must be the server.
    TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
    TK_PG_SERVER="$(tk_resolve_postgres_server_container "${POSTGRES_SERVER_CONTAINER:-auto}")" || {
      echo '{"error":"postgres server container unresolved (data role needs local PG server)"}' >&2
      exit 1
    }
    ;;
esac

mkdir -p "$(dirname "$OUT")"
: > "$OUT"
export OUT ROLE INTERVAL_S DURATION_S TK_APP TK_PG TK_PG_SERVER

python3 - <<'PY'
import json, os, subprocess, time
from pathlib import Path

out = Path(os.environ["OUT"])
role = os.environ["ROLE"]
interval_s = int(os.environ["INTERVAL_S"])
duration_s = os.environ.get("DURATION_S") or ""
deadline = time.time() + int(duration_s) if duration_s else None
tk_app = os.environ.get("TK_APP") or ""
tk_pg = os.environ.get("TK_PG") or ""
tk_pg_server = os.environ.get("TK_PG_SERVER") or ""

def sh(cmd):
    p = subprocess.run(cmd, capture_output=True, text=True, shell=isinstance(cmd, str))
    return p.returncode, (p.stdout or "").strip(), (p.stderr or "").strip()

def loadavg():
    try:
        return open("/proc/loadavg").read().split()[:3]
    except Exception:
        return []

def docker_stats(name: str):
    if not name:
        return None, None
    rc, stdout, _ = sh([
        "docker", "stats", "--no-stream",
        "--format", "{{.CPUPerc}}|{{.MemUsage}}",
        name,
    ])
    if rc != 0 or not stdout or "|" not in stdout:
        return None, None
    cpu_s, mem = stdout.split("|", 1)
    cpu = None
    if cpu_s.endswith("%"):
        try:
            cpu = float(cpu_s[:-1])
        except Exception:
            cpu = None
    return cpu, mem

def tcp_estab():
    rc, stdout, _ = sh("ss -tan state established 2>/dev/null | awk 'NR>1{c++} END{print c+0}'")
    try:
        return int(stdout or "0")
    except Exception:
        return None

def resolve_caddy():
    for name in ("caddy", "tokenkey-caddy"):
        rc, _, _ = sh(["docker", "inspect", name])
        if rc == 0:
            return name
    return ""

def pg_total_active(container: str):
    if not container:
        return None, None
    rc, stdout, _ = sh([
        "docker", "exec", container,
        "psql", "-U", "tokenkey", "-d", "tokenkey", "-Atc",
        "select count(*), count(*) filter (where state='active') from pg_stat_activity;",
    ])
    if rc != 0 or not stdout:
        return None, None
    # Prefer whitespace; tolerate accidental pipes from older samplers.
    parts = stdout.replace("|", " ").replace("\t", " ").split()
    if len(parts) < 2:
        return None, None
    try:
        return int(parts[0]), int(parts[1])
    except Exception:
        return None, None

caddy = resolve_caddy() if role == "entry" else ""
t0 = time.time()
i = 0
while True:
    if deadline is not None and time.time() >= deadline:
        break
    i += 1
    ts = int(time.time())
    la = " ".join(loadavg())
    row = {"ts": ts, "role": role, "i": i, "loadavg": la}
    if role == "app":
        cpu, mem = docker_stats(tk_app)
        row["cpu"] = None if cpu is None else f"{cpu}%"
        row["mem"] = mem
        row["inflight"] = None
    elif role == "data":
        total, active = pg_total_active(tk_pg)
        # Space-separated so JSON stays valid (no raw tabs).
        row["pg_total_active"] = None if total is None else f"{total} {active}"
        cpu, mem = docker_stats(tk_pg_server)
        row["pg_docker"] = None if cpu is None else f"{cpu}%|{mem or ''}"
    else:  # entry
        cpu, mem = docker_stats(caddy)
        row["caddy"] = caddy or None
        row["cpu"] = None if cpu is None else f"{cpu}%"
        row["mem"] = mem
        row["tcp_estab"] = tcp_estab()
    line = json.dumps(row, ensure_ascii=False)
    with out.open("a", encoding="utf-8") as f:
        f.write(line + "\n")
    print(line, flush=True)
    time.sleep(interval_s)

print(json.dumps({
    "phase": "lite_monitor_done",
    "role": role,
    "n": i,
    "duration_s": round(time.time() - t0, 2),
    "out": str(out),
}, ensure_ascii=False), flush=True)
PY
