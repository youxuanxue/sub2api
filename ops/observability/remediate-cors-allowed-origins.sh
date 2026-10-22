#!/usr/bin/env bash
# Write-capable: upsert cors.allowed_origins on the live app config.yaml and
# recreate the active app container so Quickstart/Studio browser probes work.
# Delivered via run-probe.sh. Requires APPLY=yes-cors-allowed-origins.
#
# Root cause this fixes: empty cors.allowed_origins → OPTIONS preflight 403 →
# browser "Failed to fetch" from https://tokenkey.dev → https://api.tokenkey.dev.
set -euo pipefail

APPLY="${APPLY:-}"
ROOT=/var/lib/tokenkey
ACTIVE_FILE="${ROOT}/active-color"
FORMAL_CFG="${ROOT}/formal/app/config.yaml"
APP_CFG="${ROOT}/app/config.yaml"

cfg_path() {
  if [ -f "$FORMAL_CFG" ]; then
    printf '%s' "$FORMAL_CFG"
  elif [ -f "$APP_CFG" ]; then
    printf '%s' "$APP_CFG"
  else
    echo "::error::no config.yaml under ${ROOT}/formal/app or ${ROOT}/app" >&2
    exit 1
  fi
}

# Print only the cors stanza (never dump secrets from the rest of the file).
print_cors_stanza() {
  local f="$1"
  sudo python3 - "$f" <<'PY'
import sys
from pathlib import Path
text = Path(sys.argv[1]).read_text(encoding="utf-8")
lines = text.splitlines()
start = None
for i, line in enumerate(lines):
    if line.strip() == "cors:" or line.startswith("cors:"):
        start = i
        break
if start is None:
    print("(no cors: stanza)")
    raise SystemExit(0)
end = len(lines)
for j in range(start + 1, len(lines)):
    if lines[j] and not lines[j][0].isspace() and not lines[j].startswith("#"):
        end = j
        break
print("\n".join(lines[start:end]))
PY
}

upsert_cors() {
  local f="$1"
  sudo python3 - "$f" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
wanted = [
    "https://tokenkey.dev",
    "https://callmodel.io",
]
stanza = (
    "cors:\n"
    "    allowed_origins:\n"
    + "".join(f'        - "{o}"\n' for o in wanted)
    + "    allow_credentials: true\n"
)

lines = text.splitlines(keepends=True)
start = None
for i, line in enumerate(lines):
    stripped = line.lstrip()
    if stripped.startswith("cors:") or line.startswith("cors:"):
        start = i
        break

if start is None:
    if text and not text.endswith("\n"):
        text += "\n"
    text += "\n" + stanza
else:
    end = len(lines)
    for j in range(start + 1, len(lines)):
        if lines[j] and not lines[j][0].isspace() and not lines[j].startswith("#"):
            end = j
            break
    new_lines = lines[:start] + [stanza if stanza.endswith("\n") else stanza + "\n"] + lines[end:]
    text = "".join(new_lines)

path.write_text(text, encoding="utf-8")
print(f"upserted cors into {path}")
PY
}

CFG="$(cfg_path)"
echo "=== meta ==="
echo "config=$CFG"
if [ -r "$ACTIVE_FILE" ]; then
  color="$(sed -n '1p' "$ACTIVE_FILE" | tr -d '[:space:]')"
else
  color=""
fi
echo "active-color=${color:-none}"

echo "=== before cors stanza ==="
print_cors_stanza "$CFG"

echo "=== before local OPTIONS ==="
before_code="$(sudo docker exec tokenkey-blue curl -sS -o /dev/null -w '%{http_code}' -X OPTIONS http://127.0.0.1:8080/v1/models \
  -H 'Origin: https://tokenkey.dev' \
  -H 'Access-Control-Request-Method: GET' \
  -H 'Access-Control-Request-Headers: authorization' 2>/dev/null || echo fail)"
echo "local_options_before=$before_code"

if [ "$APPLY" != "yes-cors-allowed-origins" ]; then
  echo "=== dry-run ==="
  echo '{"apply":false,"wanted_origins":["https://tokenkey.dev","https://callmodel.io"]}'
  exit 0
fi

ts="$(date +%Y%m%d-%H%M%S)"
echo "=== backup config ==="
sudo cp -a "$CFG" "${CFG}.before-cors-${ts}"
if [ -f "$APP_CFG" ] && [ "$(sudo realpath "$APP_CFG")" != "$(sudo realpath "$CFG")" ]; then
  sudo cp -a "$APP_CFG" "${APP_CFG}.before-cors-${ts}"
fi

echo "=== upsert cors ==="
upsert_cors "$CFG"
# Keep sibling copy in sync when it is a separate file (cutover layout).
if [ -f "$APP_CFG" ] && [ "$(sudo realpath "$APP_CFG" 2>/dev/null || echo "$APP_CFG")" != "$(sudo realpath "$CFG")" ]; then
  upsert_cors "$APP_CFG"
fi

echo "=== after cors stanza ==="
print_cors_stanza "$CFG"

cd "$ROOT"
if [ -n "$color" ] && [ -f docker-compose.bluegreen.yml ]; then
  case "$color" in
    blue|green) ;;
    *) echo "::error::invalid active-color=$color"; exit 1 ;;
  esac
  container="tokenkey-$color"
  compose=(sudo docker compose --project-name tokenkey --env-file .env -f docker-compose.bluegreen.yml)
else
  container=tokenkey
  compose=(sudo docker compose --env-file .env)
fi

echo "=== pre-drain $container ==="
OLD_HEALTH="$(sudo docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || echo missing)"
echo "pre-drain: outgoing container health=$OLD_HEALTH"
if [ "$OLD_HEALTH" = healthy ]; then
  sudo docker kill -s USR1 "$container" 2>/dev/null || true
  prev=-1
  stall=0
  for i in $(seq 1 15); do
    body="$(sudo docker exec "$container" wget -q -T 3 -O - http://localhost:8080/health/inflight 2>/dev/null || true)"
    n="$(printf '%s' "$body" | sed -n 's/.*"in_flight":\([0-9]*\).*/\1/p')"
    if printf '%s' "$body" | grep -q '"draining":true'; then d=true; else d=false; fi
    echo "pre-drain: draining=$d in_flight=${n:-?} try=$i/15"
    [ "$d" = true ] && [ "${n:-1}" = 0 ] && break
    if [ -n "$n" ]; then
      if [ "$prev" -ge 0 ] && [ "$n" -ge "$prev" ]; then stall=$((stall + 1)); else stall=0; fi
      prev=$n
      if [ "$stall" -ge 3 ]; then
        echo "pre-drain: in_flight plateaued at $n for 3 tries; stop waiting"
        break
      fi
    fi
    sleep 2
  done
else
  echo "pre-drain SKIPPED: outgoing container not healthy (health=$OLD_HEALTH)"
fi

echo "=== recreate $container ==="
"${compose[@]}" up -d --no-deps --force-recreate --timeout 30 "$container"

for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  s="$(sudo docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing)"
  echo "health try $i: $s"
  [ "$s" = healthy ] || [ "$s" = running ] && break
  sleep 5
done
FINAL="$(sudo docker inspect "$container" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing)"
echo "final_health=$FINAL"
if [ "$FINAL" != healthy ] && [ "$FINAL" != running ]; then
  echo "::error::container $container not healthy (final=$FINAL)"
  sudo docker logs "$container" --since 2m 2>&1 | grep -iE 'cors|error|fatal|listen' | tail -40 || true
  exit 1
fi

echo "=== after boot cors warning check ==="
# Expect NO "CORS allowed_origins not configured" after recreate.
if sudo docker logs "$container" --since 2m 2>&1 | grep -F 'CORS allowed_origins not configured' >/dev/null; then
  echo "::error::cors still unconfigured after recreate"
  exit 1
fi
echo "cors_boot_warning=absent"

echo "=== after local OPTIONS ==="
after_headers="$(sudo docker exec "$container" curl -sS -D- -o /dev/null -X OPTIONS http://127.0.0.1:8080/v1/models \
  -H 'Origin: https://tokenkey.dev' \
  -H 'Access-Control-Request-Method: GET' \
  -H 'Access-Control-Request-Headers: authorization')"
printf '%s\n' "$after_headers"
after_code="$(printf '%s\n' "$after_headers" | awk 'BEGIN{c=""} /^HTTP/{c=$2} END{print c}')"
acao="$(printf '%s\n' "$after_headers" | awk -F': ' 'tolower($1)=="access-control-allow-origin"{print $2}' | tr -d '\r')"
echo "local_options_after=$after_code acao=$acao"
if [ "$after_code" != "204" ] && [ "$after_code" != "200" ]; then
  echo "::error::expected OPTIONS 204, got $after_code"
  exit 1
fi
if [ "$acao" != "https://tokenkey.dev" ]; then
  echo "::error::expected Access-Control-Allow-Origin=https://tokenkey.dev, got '$acao'"
  exit 1
fi

echo "=== verdict ==="
echo '{"apply":true,"verdict":"cors_ok","origin":"https://tokenkey.dev","options_status":'"$after_code"'}'
