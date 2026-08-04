#!/bin/bash
# probe-traffic-logs.sh — pre-filter TokenKey gateway docker logs into the two
# files consumed by profile-traffic.py: /tmp/acc.txt and /tmp/sse.txt.
#
# Runs INSIDE the TokenKey host (prod or edge), typically via SSM. Read-only.
#
# Env:
#   SINCE       docker logs --since window. Default 1h. Accepts "30m", "2h", etc.
#               (For minutes-mode in the SKILL, caller should pass "$((MIN+2))m"
#                — +2m buffer covers minute boundaries when filtering by completed_at.)
#   PATH_KEY    path substring required on http-request-completed lines.
#               Default /v1/messages.
#   CONTAINER   gateway container name. Default auto. auto resolves
#               /var/lib/tokenkey/active-color to tokenkey-blue/green and
#               falls back to the legacy tokenkey container.
#   ACTIVE_COLOR_FILE
#               active-color file path for CONTAINER=auto
#               (default /var/lib/tokenkey/active-color; test seam).
#
# Reports line counts at the end so the caller can detect zero-match patterns
# (e.g. log-format drift renaming "http request completed" to "http_request_completed").
# 不开 `set -o pipefail`：grep 0 匹配会返回 1，pipefail+set -e 会误中止，
# 而我们正好要在 wc 看到 0 行时报 WARN。只保留 set -u 防御 unbound vars。
set -u

SINCE="${SINCE:-1h}"
PATH_KEY="${PATH_KEY:-/v1/messages}"
CONTAINER_INPUT="${CONTAINER:-auto}"

# Canonical app-container resolver (ops/lib/resolve-app-container.sh) is the sole
# owner of active-color + running-candidate logic. run-probe.sh ships it next to
# this probe; the second path covers running straight from the repo.
_tk_resolver=""
for _cand in "${TK_LIB_DIR:-$(dirname "$0")}/resolve-app-container.sh" \
             "$(dirname "$0")/../lib/resolve-app-container.sh"; do
  if [ -f "$_cand" ]; then _tk_resolver="$_cand"; break; fi
done
if [ -z "$_tk_resolver" ]; then
  echo "canonical app-container resolver not found (ops/lib/resolve-app-container.sh)" >&2
  exit 1
fi
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_resolver"

# An unresolved container must not fall through to `docker logs ""`: that returns
# nothing and reads exactly like a quiet traffic window.
if ! CONTAINER="$(tk_resolve_app_container "$CONTAINER_INPUT")"; then
  echo "probe_traffic_logs container_input=$CONTAINER_INPUT container=unknown since=$SINCE path_key=$PATH_KEY acc_lines=0 sse_lines=0"
  echo "probe_traffic_logs ERROR app container unresolved (no running candidate, or several) — refusing to report zero traffic" >&2
  exit 3
fi

docker logs "$CONTAINER" --since "$SINCE" 2>&1 \
  | grep -F 'http request completed' \
  | grep -F "$PATH_KEY" \
  > /tmp/acc.txt
docker logs "$CONTAINER" --since "$SINCE" 2>&1 \
  | grep -F 'sticky.scheduler_entry' \
  > /tmp/sse.txt

ACC=$(wc -l < /tmp/acc.txt)
SSE=$(wc -l < /tmp/sse.txt)
echo "probe_traffic_logs container_input=$CONTAINER_INPUT container=$CONTAINER since=$SINCE path_key=$PATH_KEY acc_lines=$ACC sse_lines=$SSE"
if [ "$ACC" = "0" ] && [ "$SSE" = "0" ]; then
  echo "probe_traffic_logs WARN both files empty — check (a) docker logs uptime vs SINCE, (b) log format drift, (c) container name" >&2
fi
