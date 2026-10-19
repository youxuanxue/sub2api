#!/usr/bin/env bash
# Write-capable: raise NewAPI Chat non-stream first-output budget to 600s on a
# blue/green host WITHOUT draining the live upstream first.
#
# Safe path (default):
#   1) upsert .env + patch compose fallbacks
#   2) recreate INACTIVE color on the same image as ACTIVE
#   3) Caddy hot-cutover to inactive
#   4) recreate previous color so both sides carry 600
#
# Delivered via run-probe.sh. Requires:
#   APPLY=yes-newapi-nonstream-timeout
#
# Optional:
#   WANT_TIMEOUT=600   (must be 1..900; default 600 — current prod/cn-prod pin)
set -euo pipefail

APPLY="${APPLY:-}"
WANT_TIMEOUT="${WANT_TIMEOUT:-600}"
KEY=GATEWAY_NEWAPI_CHAT_NONSTREAM_FIRST_OUTPUT_TIMEOUT

case "$WANT_TIMEOUT" in
  ''|*[!0-9]*) echo "::error::WANT_TIMEOUT must be int"; exit 2 ;;
esac
if [ "$WANT_TIMEOUT" -lt 1 ] || [ "$WANT_TIMEOUT" -gt 900 ]; then
  echo "::error::WANT_TIMEOUT out of allowed range 1..900 (got $WANT_TIMEOUT)"
  exit 2
fi

ACTIVE_FILE=/var/lib/tokenkey/active-color
CADDY_DIR=/var/lib/tokenkey/caddy
LIVE_CADDY="${CADDY_DIR}/Caddyfile"

if [ -f /var/lib/tokenkey/docker-compose.bluegreen.yml ]; then
  HOST_KIND=aws
  ROOT=/var/lib/tokenkey
  ENV_FILE="${ROOT}/.env"
  COMPOSE_BG="${ROOT}/docker-compose.bluegreen.yml"
elif [ -f /opt/tokenkey/compose.yml ]; then
  HOST_KIND=cn
  ROOT=/opt/tokenkey
  ENV_FILE="${ROOT}/.env"
  COMPOSE_BG="${ROOT}/compose.yml"
else
  echo "::error::unknown host layout (neither aws bluegreen nor cn compose.yml)"
  exit 1
fi

[[ -r "$ACTIVE_FILE" ]] || { echo "::error::missing $ACTIVE_FILE"; exit 1; }
[[ -f "$LIVE_CADDY" ]] || { echo "::error::missing $LIVE_CADDY"; exit 1; }
[[ -f "$ENV_FILE" ]] || { echo "::error::missing $ENV_FILE"; exit 1; }

ACTIVE="$(sed -n '1p' "$ACTIVE_FILE" | tr -d '[:space:]')"
case "$ACTIVE" in
  blue) INACTIVE=green ;;
  green) INACTIVE=blue ;;
  *) echo "::error::invalid active-color=$ACTIVE"; exit 1 ;;
esac
ACTIVE_CTR="tokenkey-${ACTIVE}"
INACTIVE_CTR="tokenkey-${INACTIVE}"

upsert_env() {
  local key="$1" val="$2"
  if sudo grep -q "^${key}=" "$ENV_FILE"; then
    sudo sed -i "s|^${key}=.*|${key}=${val}|" "$ENV_FILE"
  else
    printf '%s=%s\n' "$key" "$val" | sudo tee -a "$ENV_FILE" >/dev/null
  fi
}

patch_compose_fallback() {
  local f="$1"
  [[ -f "$f" ]] || return 0
  sudo python3 - "$f" "$KEY" "$WANT_TIMEOUT" <<'PY'
import pathlib, re, sys
path = pathlib.Path(sys.argv[1])
key, want = sys.argv[2], sys.argv[3]
text = path.read_text()
pattern = rf"({re.escape(key)}=\$\{{\s*{re.escape(key)}\s*:-\s*)\d+(\s*\}})"
new, n = re.subn(pattern, rf"\g<1>{want}\2", text)
if n == 0:
    # also accept already-expanded "KEY=600" compose lines without interpolation
    pattern2 = rf"(-\s*{re.escape(key)}=)\d+"
    new, n = re.subn(pattern2, rf"\g<1>{want}", text)
if n == 0:
    raise SystemExit(f"compose missing {key} fallback to patch: {path}")
path.write_text(new)
print(f"patched {path} occurrences={n}")
PY
}

container_key_env() {
  sudo docker inspect "$1" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk -v k="$KEY" -F= '$1==k {print; found=1} END {exit found?0:1}'
}

wait_healthy() {
  local name="$1" i s
  for i in $(seq 1 36); do
    s="$(sudo docker inspect "$name" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing)"
    echo "health $name try $i: $s"
    [ "$s" = healthy ] && return 0
    sleep 5
  done
  return 1
}

health_http_ok() {
  local name="$1" code
  code="$(sudo docker exec "$name" wget -q -T 3 -S -O - http://localhost:8080/health 2>&1 | awk '/^  HTTP/{print $2; exit}')"
  echo "http_health $name code=${code:-?}"
  [ "$code" = 200 ]
}

render_caddy_with_upstream() {
  local upstream="$1" tmp="$2"
  sudo awk -v upstream="${upstream}" '
    /^[[:space:]]*reverse_proxy[[:space:]]+/ && $0 ~ /\{[[:space:]]*$/ {
      count += 1
      if (count == 1) {
        match($0, /[^[:space:]]/)
        indent = RSTART > 1 ? substr($0, 1, RSTART - 1) : ""
        print indent "reverse_proxy " upstream " {"
      } else {
        print
      }
      next
    }
    { print }
    END { if (count != 1) exit 7 }
  ' "${LIVE_CADDY}" | sudo tee "${tmp}" >/dev/null
}

caddy_validate_image() {
  sudo docker inspect tokenkey-caddy --format '{{.Config.Image}}'
}

commit_cutover() {
  local color="$1" upstream tmp backup ts img
  upstream="tokenkey-${color}:8080"
  tmp="${CADDY_DIR}/Caddyfile.nonstream-${color}.new"
  ts="$(date +%Y%m%d-%H%M%S)"
  backup="${CADDY_DIR}/Caddyfile.before-nonstream-${color}-${ts}"
  img="$(caddy_validate_image)"

  render_caddy_with_upstream "${upstream}" "${tmp}"
  if ! sudo docker run --rm -v "${tmp}:/tmp/Caddyfile:ro" "${img}" \
      caddy validate --config /tmp/Caddyfile --adapter caddyfile; then
    sudo rm -f "${tmp}"
    echo "::error::caddy validate failed for ${upstream}"
    return 1
  fi
  sudo cp -a "${LIVE_CADDY}" "${backup}"
  sudo sh -c "cat '${tmp}' > '${LIVE_CADDY}'"
  sudo rm -f "${tmp}"
  if ! sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
    sudo sh -c "cat '${backup}' > '${LIVE_CADDY}'"
    sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || true
    echo "::error::caddy reload failed; restored backup"
    return 1
  fi
  printf '%s\n' "${color}" | sudo tee "${ACTIVE_FILE}" >/dev/null
  # Keep Worker control DNS name `tokenkey` pointed at the active color
  # (same contract as ops/stage0/deploy_via_ssm_bluegreen.sh commit_cutover).
  if ! sync_gemini_web_control_alias "${color}"; then
    if sudo docker inspect tokenkey-gemini-web >/dev/null 2>&1; then
      echo "::error::gemini-web control alias sync failed while Worker is present"
      return 1
    fi
    echo "::warning::gemini-web control alias sync failed color=${color}"
  fi
  echo "cutover_ok upstream=${upstream} backup=${backup}"
}

sync_gemini_web_control_alias() {
  local color="$1"
  # Prefer co-located reviewed script when present (run-probe may upload it).
  if [[ -x /tmp/sync-control-alias-on-host.sh ]]; then
    TK_ACTIVE_COLOR="${color}" bash /tmp/sync-control-alias-on-host.sh
    return $?
  fi
  TK_ACTIVE_COLOR="${color}" bash -s <<'ALIAS_SH'
set -euo pipefail
NETWORK="${TK_NETWORK:-tokenkey_tokenkey-network}"
PROXY_NAME=tokenkey
SOCAT_IMAGE="${TK_CONTROL_PROXY_IMAGE:-alpine/socat:1.8.0.0}"
color="${TK_ACTIVE_COLOR:?}"
target="tokenkey-${color}"
sudo docker inspect "${target}" >/dev/null
if sudo docker inspect "${PROXY_NAME}" >/dev/null 2>&1; then
  image="$(sudo docker inspect "${PROXY_NAME}" --format '{{.Config.Image}}' 2>/dev/null || true)"
  if [[ "${image}" == *sub2api* && "${image}" != *socat* && "${image}" != *gemini-web* ]]; then
    echo "gemini-web-control-alias: legacy app container named tokenkey; leaving intact"
    exit 0
  fi
  desired="TCP:${target}:8080"
  current="$(sudo docker inspect "${PROXY_NAME}" --format '{{json .Config.Cmd}}' 2>/dev/null || true)"
  running="$(sudo docker inspect "${PROXY_NAME}" --format '{{.State.Running}}' 2>/dev/null || true)"
  if [[ "${running}" == "true" && "${current}" == *"${desired}"* ]]; then
    echo "gemini-web-control-alias: already proxies ${desired}"
    exit 0
  fi
  sudo docker rm -f "${PROXY_NAME}" >/dev/null
fi
sudo docker image inspect "${SOCAT_IMAGE}" >/dev/null 2>&1 || sudo docker pull "${SOCAT_IMAGE}" >/dev/null
sudo docker run -d --name "${PROXY_NAME}" --restart unless-stopped \
  --network "${NETWORK}" --user 65534:65534 --read-only --cap-drop ALL \
  --security-opt no-new-privileges --memory 32m --memory-swap 32m --cpus 0.1 --pids-limit 32 \
  "${SOCAT_IMAGE}" TCP-LISTEN:8080,fork,reuseaddr "TCP:${target}:8080" >/dev/null
echo "tk_gemini_web_control_alias: OK color=${color} target=${target}"
ALIAS_SH
}

echo "=== meta ==="
echo "host_kind=${HOST_KIND} root=${ROOT} active=${ACTIVE} inactive=${INACTIVE} want=${KEY}=${WANT_TIMEOUT}"

echo "=== before .env ==="
sudo awk -v k="$KEY" -F= '$1==k {print}' "$ENV_FILE" || echo "(missing)"
echo "=== before active container env ==="
container_key_env "$ACTIVE_CTR" || echo "(missing on $ACTIVE_CTR)"
echo "=== before inactive container env ==="
container_key_env "$INACTIVE_CTR" 2>/dev/null || echo "(missing/stopped on $INACTIVE_CTR)"
echo "=== before caddy upstream ==="
sudo grep -nE 'reverse_proxy[[:space:]]+tokenkey' "$LIVE_CADDY" | head -5
echo "=== inflight active (must stay serving until cutover) ==="
sudo docker exec "$ACTIVE_CTR" wget -q -T 3 -O - http://localhost:8080/health/inflight 2>/dev/null || true
echo

if [ "$APPLY" != "yes-newapi-nonstream-timeout" ]; then
  echo "=== dry-run ==="
  echo "{\"apply\":false,\"host_kind\":\"${HOST_KIND}\",\"active\":\"${ACTIVE}\",\"inactive\":\"${INACTIVE}\",\"want\":${WANT_TIMEOUT}}"
  exit 0
fi

echo "=== backup ==="
ts="$(date +%Y%m%d-%H%M%S)"
sudo cp -a "$ENV_FILE" "${ENV_FILE}.before-nonstream-timeout-${ts}"
sudo cp -a "$COMPOSE_BG" "${COMPOSE_BG}.before-nonstream-timeout-${ts}"
sudo cp -a "$LIVE_CADDY" "${LIVE_CADDY}.before-nonstream-timeout-${ts}"

echo "=== upsert .env ==="
upsert_env "$KEY" "$WANT_TIMEOUT"
sudo awk -v k="$KEY" -F= '$1==k {print}' "$ENV_FILE"

echo "=== patch compose fallbacks ==="
patch_compose_fallback "$COMPOSE_BG"
if [ "$HOST_KIND" = aws ]; then
  # keep both color image pins on the currently active tag so inactive recreate
  # does not silently roll back a version.
  case "$ACTIVE" in
    blue) active_img_key=TOKENKEY_IMAGE_BLUE ;;
    green) active_img_key=TOKENKEY_IMAGE_GREEN ;;
  esac
  active_img="$(sudo awk -F= -v k="$active_img_key" '$1==k {print $2; exit}' "$ENV_FILE")"
  if [ -z "$active_img" ]; then
    active_img="$(sudo awk -F= '$1=="TOKENKEY_IMAGE" {print $2; exit}' "$ENV_FILE")"
  fi
  [ -n "$active_img" ] || { echo "::error::cannot resolve active image pin"; exit 1; }
  upsert_env "TOKENKEY_IMAGE_BLUE" "$active_img"
  upsert_env "TOKENKEY_IMAGE_GREEN" "$active_img"
  echo "aligned TOKENKEY_IMAGE_BLUE/GREEN=${active_img}"
fi

bring_up_inactive_aws() {
  cd "$ROOT"
  sudo docker compose --project-name tokenkey --env-file .env -f docker-compose.bluegreen.yml \
    up -d --no-deps --force-recreate --timeout 60 "$INACTIVE_CTR"
}

bring_up_inactive_cn() {
  # Mirror deploy/domestic/bluegreen.py candidate_compose: clone the live
  # container identity onto the standby color with the alternate host port
  # (blue=18081, green=18082) and an explicit timeout override.
  local work compose_out
  work="${ROOT}/.remediate-nonstream-${WANT_TIMEOUT}-${ts}"
  sudo mkdir -p "$work"
  compose_out="${work}/compose.json"
  sudo python3 - "$ACTIVE_CTR" "$INACTIVE_CTR" "$compose_out" "$KEY" "$WANT_TIMEOUT" <<'PY'
import json, pathlib, subprocess, sys
active, target, dst, key, want = sys.argv[1:6]
insp = json.loads(subprocess.check_output(["docker", "inspect", active], text=True))[0]
env = dict(item.split("=", 1) for item in insp["Config"]["Env"])
env[key] = want
# Compose must not re-interpolate literal dollars from secrets/config.
env = {k: v.replace("$", "$$") for k, v in env.items()}
networks = insp["NetworkSettings"]["Networks"]
if len(networks) != 1:
    raise SystemExit(f"expected one app network, got {list(networks)}")
net_name = next(iter(networks))
published = "18081" if target.endswith("blue") else "18082"
# Prefer the live image id so standby matches serving bits exactly.
image = insp["Image"]
mounts = []
for m in insp["Mounts"]:
    item = {
        "type": m["Type"].lower(),
        "source": m["Source"],
        "target": m["Destination"],
    }
    if not m.get("RW", True):
        item["read_only"] = True
    mounts.append(item)
svc = {
    "image": image,
    "container_name": target,
    "pull_policy": "never",
    "restart": "unless-stopped",
    "environment": env,
    "volumes": mounts,
    "ports": [{
        "target": 8080,
        "published": published,
        "host_ip": "127.0.0.1",
        "protocol": "tcp",
    }],
    "networks": ["existing"],
}
# Preserve healthcheck from the live container when present.
hc = insp["Config"].get("Healthcheck")
if hc:
    svc["healthcheck"] = {
        "test": hc.get("Test") or ["CMD", "wget", "-q", "-O", "-", "http://localhost:8080/health"],
        "interval": "10s",
        "timeout": "5s",
        "retries": 12,
        "start_period": "40s",
    }
doc = {
    "services": {target: svc},
    "networks": {"existing": {"external": True, "name": net_name}},
}
pathlib.Path(dst).write_text(json.dumps(doc, indent=2) + "\n")
print(f"wrote {dst} service={target} published={published} image={image[:19]}…")
PY
  sudo docker rm -f "$INACTIVE_CTR" >/dev/null 2>&1 || true
  sudo docker compose -p cn-prod-bluegreen --project-directory "$work" \
    -f "$compose_out" up -d --no-deps --timeout 60 "$INACTIVE_CTR"
}

echo "=== bring up inactive ${INACTIVE_CTR} (live ${ACTIVE_CTR} untouched) ==="
if [ "$HOST_KIND" = aws ]; then
  bring_up_inactive_aws
else
  bring_up_inactive_cn
fi

wait_healthy "$INACTIVE_CTR" || {
  echo "::error::inactive not healthy"
  sudo docker logs "$INACTIVE_CTR" --since 3m 2>&1 | tail -60 || true
  exit 1
}
health_http_ok "$INACTIVE_CTR" || {
  echo "::error::inactive /health not HTTP 200"
  exit 1
}
got="$(container_key_env "$INACTIVE_CTR" || true)"
echo "inactive env: $got"
if [ "$got" != "${KEY}=${WANT_TIMEOUT}" ]; then
  echo "::error::inactive missing ${KEY}=${WANT_TIMEOUT} (got ${got:-empty})"
  exit 1
fi

echo "=== cutover to ${INACTIVE} (active ${ACTIVE} still up until reload) ==="
commit_cutover "$INACTIVE" || exit 1

# Confirm routed health via the now-active container.
NEW_ACTIVE="$INACTIVE"
NEW_ACTIVE_CTR="tokenkey-${NEW_ACTIVE}"
OLD_CTR="tokenkey-${ACTIVE}"
for i in 1 2 3 4 5 6; do
  if health_http_ok "$NEW_ACTIVE_CTR"; then
    break
  fi
  sleep 2
done
health_http_ok "$NEW_ACTIVE_CTR" || {
  echo "::error::post-cutover health failed on $NEW_ACTIVE_CTR"
  exit 1
}

drain_standby() {
  local name="$1" max_tries="${2:-90}" i body n d prev=-1 stall=0
  echo "=== drain standby ${name} (no longer routed; finish in-flight before recreate) ==="
  sudo docker kill -s USR1 "$name" 2>/dev/null || true
  for i in $(seq 1 "$max_tries"); do
    body="$(sudo docker exec "$name" wget -q -T 3 -O - http://localhost:8080/health/inflight 2>/dev/null || true)"
    n="$(printf '%s' "$body" | sed -n 's/.*"in_flight":\([0-9]*\).*/\1/p')"
    if printf '%s' "$body" | grep -q '"draining":true'; then d=true; else d=false; fi
    echo "standby-drain: draining=$d in_flight=${n:-?} try=$i/${max_tries}"
    [ "$d" = true ] && [ "${n:-1}" = 0 ] && return 0
    if [ -n "$n" ]; then
      if [ "$prev" -ge 0 ] && [ "$n" -ge "$prev" ]; then stall=$((stall + 1)); else stall=0; fi
      prev=$n
      # Long non-stream work can sit near the previous 300s budget; keep waiting
      # unless we are truly stuck with no progress for a long plateau.
      if [ "$stall" -ge 30 ]; then
        echo "::warning::standby in_flight plateaued at $n; proceeding to stop/recreate"
        return 0
      fi
    fi
    sleep 2
  done
  echo "::warning::standby drain wait exhausted; proceeding"
  return 0
}

# Up to ~6 minutes: cover prior 300s non-stream budget for leftover attempts.
drain_standby "$OLD_CTR" 180

echo "=== recreate previous color ${OLD_CTR} so both sides carry ${WANT_TIMEOUT} ==="
if [ "$HOST_KIND" = aws ]; then
  cd "$ROOT"
  sudo docker compose --project-name tokenkey --env-file .env -f docker-compose.bluegreen.yml \
    up -d --no-deps --force-recreate --timeout 60 "$OLD_CTR"
else
  # Rebuild the drained previous color from the now-serving container identity.
  work="${ROOT}/.remediate-nonstream-${WANT_TIMEOUT}-${ts}"
  old_compose="${work}/compose-old.json"
  sudo python3 - "$NEW_ACTIVE_CTR" "$OLD_CTR" "$old_compose" "$KEY" "$WANT_TIMEOUT" <<'PY'
import json, pathlib, subprocess, sys
active, target, dst, key, want = sys.argv[1:6]
insp = json.loads(subprocess.check_output(["docker", "inspect", active], text=True))[0]
env = dict(item.split("=", 1) for item in insp["Config"]["Env"])
env[key] = want
env = {k: v.replace("$", "$$") for k, v in env.items()}
networks = insp["NetworkSettings"]["Networks"]
if len(networks) != 1:
    raise SystemExit(f"expected one app network, got {list(networks)}")
net_name = next(iter(networks))
published = "18081" if target.endswith("blue") else "18082"
image = insp["Image"]
mounts = []
for m in insp["Mounts"]:
    item = {
        "type": m["Type"].lower(),
        "source": m["Source"],
        "target": m["Destination"],
    }
    if not m.get("RW", True):
        item["read_only"] = True
    mounts.append(item)
svc = {
    "image": image,
    "container_name": target,
    "pull_policy": "never",
    "restart": "unless-stopped",
    "environment": env,
    "volumes": mounts,
    "ports": [{
        "target": 8080,
        "published": published,
        "host_ip": "127.0.0.1",
        "protocol": "tcp",
    }],
    "networks": ["existing"],
}
hc = insp["Config"].get("Healthcheck")
if hc:
    svc["healthcheck"] = {
        "test": hc.get("Test") or ["CMD", "wget", "-q", "-O", "-", "http://localhost:8080/health"],
        "interval": "10s",
        "timeout": "5s",
        "retries": 12,
        "start_period": "40s",
    }
doc = {
    "services": {target: svc},
    "networks": {"existing": {"external": True, "name": net_name}},
}
pathlib.Path(dst).write_text(json.dumps(doc, indent=2) + "\n")
print(f"wrote {dst} service={target} published={published}")
PY
  sudo docker rm -f "$OLD_CTR" >/dev/null 2>&1 || true
  sudo docker compose -p cn-prod-bluegreen --project-directory "$work" \
    -f "$old_compose" up -d --no-deps --timeout 60 "$OLD_CTR"
fi

wait_healthy "$OLD_CTR" || echo "::warning::old color not healthy yet (non-serving); inspect later"
old_got="$(container_key_env "$OLD_CTR" || true)"
echo "old color env: ${old_got:-missing}"

echo "=== after verify ==="
echo "active-color=$(sudo cat "$ACTIVE_FILE" | tr -d '[:space:]')"
sudo grep -nE 'reverse_proxy[[:space:]]+tokenkey' "$LIVE_CADDY" | head -5
new_got="$(container_key_env "$NEW_ACTIVE_CTR")"
echo "serving env: $new_got"
if [ "$new_got" != "${KEY}=${WANT_TIMEOUT}" ]; then
  echo "::error::serving container env mismatch"
  exit 1
fi
if [ "$old_got" != "${KEY}=${WANT_TIMEOUT}" ]; then
  echo "::error::standby container env mismatch"
  exit 1
fi

echo "=== ok ==="
python3 - <<PY
import json
print(json.dumps({
  "apply": True,
  "host_kind": "${HOST_KIND}",
  "serving": "${NEW_ACTIVE_CTR}",
  "standby": "${OLD_CTR}",
  "timeout": ${WANT_TIMEOUT},
  "key": "${KEY}",
}))
PY
