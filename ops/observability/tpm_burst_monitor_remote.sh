#!/usr/bin/env bash
# Remote host sampler for TPM burst formal runs (cn-prod via run-probe).
# Emits JSON lines + a final summary. No secrets.
#
# Env:
#   DURATION_S   sample window seconds (default 180)
#   INTERVAL_S   sleep between samples (default 2)
set -euo pipefail

DURATION_S="${DURATION_S:-180}"
INTERVAL_S="${INTERVAL_S:-2}"
case "$DURATION_S" in ''|*[!0-9]*) echo '{"error":"bad DURATION_S"}'; exit 2 ;; esac
case "$INTERVAL_S" in ''|*[!0-9]*) echo '{"error":"bad INTERVAL_S"}'; exit 2 ;; esac
if [ "$DURATION_S" -lt 5 ] || [ "$INTERVAL_S" -lt 1 ]; then
  echo '{"error":"DURATION_S>=5 and INTERVAL_S>=1 required"}'
  exit 2
fi

APP_CONTAINER=tokenkey
if [ -r /var/lib/tokenkey/active-color ]; then
  color="$(tr -d '[:space:]' < /var/lib/tokenkey/active-color)"
  case "$color" in blue|green) APP_CONTAINER="tokenkey-${color}" ;; esac
fi
export DURATION_S INTERVAL_S APP_CONTAINER

echo "=== monitor_meta ==="
python3 - <<'PY'
import json, os, time
print(json.dumps({
  "phase": "monitor_start",
  "host": os.uname().nodename,
  "duration_s": int(os.environ["DURATION_S"]),
  "interval_s": int(os.environ["INTERVAL_S"]),
  "app_container": os.environ["APP_CONTAINER"],
  "started_at_unix": time.time(),
}, ensure_ascii=False))
PY

python3 - <<'PY'
import json, os, subprocess, time, re

duration_s = int(os.environ.get("DURATION_S", "180"))
interval_s = int(os.environ.get("INTERVAL_S", "2"))
app = os.environ.get("APP_CONTAINER", "tokenkey")
psql = ["docker", "exec", "tokenkey-postgres", "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t"]

def sh(cmd):
    p = subprocess.run(cmd, capture_output=True, text=True, shell=isinstance(cmd, str))
    return p.returncode, p.stdout.strip(), p.stderr.strip()

def read_loadavg():
    try:
        return open("/proc/loadavg").read().split()[:3]
    except Exception:
        return []

def read_mem_available_kb():
    try:
        for line in open("/proc/meminfo"):
            if line.startswith("MemAvailable:"):
                return int(line.split()[1])
    except Exception:
        return None
    return None

def docker_stats(name):
    rc, out, _ = sh(["docker", "stats", "--no-stream", "--format", "{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}\t{{.BlockIO}}\t{{.PIDs}}", name])
    if rc != 0 or not out:
        return None
    parts = out.split("\t")
    if len(parts) < 6:
        return None
    cpu = parts[1].replace("%", "")
    try:
        cpu_f = float(cpu)
    except Exception:
        cpu_f = None
    mem = parts[2]  # e.g. 96.1MiB / 3.822GiB
    return {"name": parts[0], "cpu_pct": cpu_f, "mem_usage": mem, "net_io": parts[3], "block_io": parts[4], "pids": parts[5]}

def ss_estab():
    rc, out, _ = sh("ss -tan state established 2>/dev/null | awk 'NR>1{c++} END{print c+0}'")
    try:
        return int(out or "0")
    except Exception:
        return None

def pg_conn():
    rc, out, _ = sh(psql + ["-c", "SELECT row_to_json(t) FROM (SELECT count(*) AS total, count(*) FILTER (WHERE state='active') AS active, count(*) FILTER (WHERE state='idle') AS idle FROM pg_stat_activity WHERE datname='tokenkey') t;"])
    if rc != 0 or not out:
        return None
    try:
        return json.loads(out.splitlines()[0])
    except Exception:
        return None

def redis_clients():
    rc, out, _ = sh("docker exec tokenkey-redis redis-cli INFO clients 2>/dev/null")
    if rc != 0:
        return None
    d = {}
    for line in out.splitlines():
        if ":" in line and not line.startswith("#"):
            k, v = line.split(":", 1)
            d[k.strip()] = v.strip()
    try:
        return {"connected_clients": int(d.get("connected_clients", "0")), "blocked_clients": int(d.get("blocked_clients", "0"))}
    except Exception:
        return d

samples = []
t0 = time.time()
deadline = t0 + duration_s
i = 0
while time.time() < deadline:
    i += 1
    row = {
        "i": i,
        "t_unix": time.time(),
        "elapsed_s": round(time.time() - t0, 2),
        "loadavg": read_loadavg(),
        "mem_available_kb": read_mem_available_kb(),
        "tcp_estab": ss_estab(),
        "app": docker_stats(app),
        "postgres": docker_stats("tokenkey-postgres"),
        "redis": docker_stats("tokenkey-redis"),
        "caddy": docker_stats("tokenkey-caddy"),
        "pg_conn": pg_conn(),
        "redis_clients": redis_clients(),
    }
    samples.append(row)
    print(json.dumps({"phase": "monitor_sample", **row}, ensure_ascii=False), flush=True)
    # sleep but don't overrun much
    remain = deadline - time.time()
    if remain <= 0:
        break
    time.sleep(min(interval_s, remain))

def peak(vals):
    vals = [v for v in vals if v is not None]
    return max(vals) if vals else None

summary = {
    "phase": "monitor_summary",
    "n_samples": len(samples),
    "duration_s_actual": round(time.time() - t0, 2),
    "peak_loadavg_1m": peak([float(s["loadavg"][0]) for s in samples if s.get("loadavg")]),
    "min_mem_available_kb": min([s["mem_available_kb"] for s in samples if s.get("mem_available_kb") is not None], default=None),
    "peak_tcp_estab": peak([s.get("tcp_estab") for s in samples]),
    "peak_app_cpu_pct": peak([(s.get("app") or {}).get("cpu_pct") for s in samples]),
    "peak_pg_cpu_pct": peak([(s.get("postgres") or {}).get("cpu_pct") for s in samples]),
    "peak_pg_total_conn": peak([((s.get("pg_conn") or {}).get("total")) for s in samples]),
    "peak_pg_active_conn": peak([((s.get("pg_conn") or {}).get("active")) for s in samples]),
    "peak_redis_clients": peak([((s.get("redis_clients") or {}).get("connected_clients")) for s in samples]),
    "app_mem_usage_last": ((samples[-1].get("app") or {}).get("mem_usage") if samples else None),
}
print(json.dumps(summary, ensure_ascii=False), flush=True)
print("=== monitor_done ===", flush=True)
PY
