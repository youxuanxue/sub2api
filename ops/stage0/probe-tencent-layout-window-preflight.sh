#!/usr/bin/env bash
# Read-only preflight before layout window cutover.
set -euo pipefail
emit() { python3 -c 'import json,sys; print(json.dumps({"k":sys.argv[1],"v":sys.argv[2]}, ensure_ascii=False))' "$1" "$2"; }

emit "health_local" "$(sudo docker exec tk-cutover-prod-app wget -q -O - http://127.0.0.1:8080/health 2>/dev/null | head -c 200 || true)"
emit "ready_env" "$([ -f /var/lib/tokenkey/.env.stage0-ready ] && echo present || echo missing)"
emit "ready_bytes" "$(sudo wc -c </var/lib/tokenkey/.env.stage0-ready 2>/dev/null | tr -d ' ' || echo 0)"
emit "live_env_keys" "$(sudo grep -E '^[A-Z0-9_]+=' /var/lib/tokenkey/.env 2>/dev/null | cut -d= -f1 | paste -sd, - || true)"
emit "caddyfile_head" "$(sudo head -n 40 /srv/tokenkey-rehearsal/formal/caddy/Caddyfile.cutover 2>/dev/null | tr '\n' '|' || true)"
emit "caddy_upstream_grep" "$(sudo grep -nE 'reverse_proxy|tokenkey|8080|cutover' /srv/tokenkey-rehearsal/formal/caddy/Caddyfile.cutover 2>/dev/null | tr '\n' '|' || true)"
emit "app_env_database" "$(sudo docker exec tk-cutover-prod-app printenv DATABASE_HOST 2>/dev/null || true)"
emit "app_env_redis" "$(sudo docker exec tk-cutover-prod-app printenv REDIS_HOST 2>/dev/null || true)"
emit "containers" "$(sudo docker ps --format '{{.Names}}:{{.Status}}' | paste -sd, -)"
emit "staging_runbook" "$([ -f /var/lib/tokenkey/layout-staging/WINDOW_RUNBOOK.md ] && echo present || echo missing)"
emit "summary" "window_preflight_done"
