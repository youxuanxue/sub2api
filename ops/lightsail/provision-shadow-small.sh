#!/usr/bin/env bash
# Phase A: stand up a sidecar small_3_0 next to a live Lightsail Edge.
# Does NOT stop the live instance, does NOT move the production Static IP,
# and does NOT write /tokenkey/lightsail/<edge_id>/*.
#
# Usage:
#   bash ops/lightsail/provision-shadow-small.sh us3
set -euo pipefail

LIVE_EDGE_ID="${1:?usage: $0 <live_edge_id>}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MATRIX="${REPO_ROOT}/deploy/aws/lightsail/edge-targets-lightsail.json"
PRECIOUS_TABLES=(users accounts api_keys groups settings usage_billing_dedup)

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }
TARGET_JSON="$(python3 "${REPO_ROOT}/deploy/aws/lightsail/resolve-edge-lightsail-target.py" --edge-id "$LIVE_EDGE_ID")"
field() { printf '%s\n' "$TARGET_JSON" | awk -F= -v k="$1" '$1==k {print substr($0, index($0,"=")+1); exit}'; }

REGION="$(field lightsail_region)"
AZ="$(field availability_zone)"
LIVE_NAME="$(field instance_name)"
LIVE_IP_NAME="$(field static_ip_name)"
DOMAIN="$(field domain)"
LIVE_PREFIX="$(field ssm_prefix)"
SHADOW_NAME="${LIVE_NAME}-s30"
SHADOW_IP_NAME="${LIVE_NAME}-s30-ip"
SHADOW_EDGE_TAG="${LIVE_EDGE_ID}-s30"
HYBRID_ROLE="tokenkey-lightsail-ssm-hybrid-${LIVE_EDGE_ID}"
IMAGE_TAG="1.8.177"
GHCR_OWNER="youxuanxue"
ACME_EMAIL="${ACME_EMAIL:-forsurexue@gmail.com}"
MAIN_GW_CIDR="${MAIN_GATEWAY_ALLOWED_CIDR:-34.194.234.88/32}"
DUMP_S3="s3://tokenkey-prod-pgdump-682751977094/edge/${LIVE_EDGE_ID}/pgdump"

if [[ "$SHADOW_NAME" == "$LIVE_NAME" || "$SHADOW_IP_NAME" == "$LIVE_IP_NAME" ]]; then
  echo "refusing shadow names that collide with live" >&2
  exit 1
fi

live_bundle="$(aws lightsail get-instance --region "$REGION" --instance-name "$LIVE_NAME" --query 'instance.bundleId' --output text)"
live_state="$(aws lightsail get-instance --region "$REGION" --instance-name "$LIVE_NAME" --query 'instance.state.name' --output text)"
live_ip="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" --query 'staticIp.ipAddress' --output text)"
live_attached="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" --query 'staticIp.attachedTo' --output text)"
live_mi="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/ssm_managed_instance_id" --query 'Parameter.Value' --output text)"
live_param_name="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/instance_name" --query 'Parameter.Value' --output text)"
live_param_ip="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/public_ip" --query 'Parameter.Value' --output text)"

assert_live_untouched() {
  local mi name ip attached
  mi="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/ssm_managed_instance_id" --query 'Parameter.Value' --output text)"
  name="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/instance_name" --query 'Parameter.Value' --output text)"
  ip="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/public_ip" --query 'Parameter.Value' --output text)"
  attached="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" --query 'staticIp.attachedTo' --output text)"
  [[ "$mi" == "$live_mi" && "$name" == "$live_param_name" && "$ip" == "$live_param_ip" ]] || {
    echo "LIVE SSM PARAMS CHANGED — abort" >&2
    exit 1
  }
  [[ "$attached" == "$LIVE_NAME" ]] || {
    echo "LIVE STATIC IP MOVED — abort attachedTo=$attached" >&2
    exit 1
  }
}

ssm_run() {
  local instance_id="$1" comment="$2" script_file="$3"
  local params cmd_id status
  params="$(mktemp)"
  python3 - "$script_file" "$params" <<'PY'
import json, sys
script = open(sys.argv[1], encoding="utf-8").read()
json.dump({"commands": [script]}, open(sys.argv[2], "w", encoding="utf-8"))
PY
  cmd_id="$(aws ssm send-command --region "$REGION" --instance-ids "$instance_id" \
    --document-name AWS-RunShellScript --comment "$comment" \
    --timeout-seconds 900 --parameters "file://${params}" \
    --query 'Command.CommandId' --output text)"
  rm -f "$params"
  local deadline=$(( $(date +%s) + 900 ))
  while [[ $(date +%s) -lt $deadline ]]; do
    status="$(aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
      --instance-id "$instance_id" --query 'Status' --output text 2>/dev/null || echo Pending)"
    case "$status" in
      Success) return 0 ;;
      Failed|Cancelled|TimedOut)
        aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
          --instance-id "$instance_id" --query '{out:StandardOutputContent,err:StandardErrorContent}' --output json >&2
        echo "ssm command failed status=$status comment=$comment" >&2
        return 1
        ;;
    esac
    sleep 5
  done
  echo "ssm command timed out comment=$comment" >&2
  return 1
}

ssm_out() {
  local instance_id="$1" comment="$2" script_file="$3"
  local params cmd_id
  params="$(mktemp)"
  python3 - "$script_file" "$params" <<'PY'
import json, sys
script = open(sys.argv[1], encoding="utf-8").read()
json.dump({"commands": [script]}, open(sys.argv[2], "w", encoding="utf-8"))
PY
  cmd_id="$(aws ssm send-command --region "$REGION" --instance-ids "$instance_id" \
    --document-name AWS-RunShellScript --comment "$comment" \
    --timeout-seconds 900 --parameters "file://${params}" \
    --query 'Command.CommandId' --output text)"
  rm -f "$params"
  local deadline=$(( $(date +%s) + 900 )) status
  while [[ $(date +%s) -lt $deadline ]]; do
    status="$(aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
      --instance-id "$instance_id" --query 'Status' --output text 2>/dev/null || echo Pending)"
    case "$status" in
      Success)
        aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
          --instance-id "$instance_id" --query 'StandardOutputContent' --output text
        return 0
        ;;
      Failed|Cancelled|TimedOut)
        aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
          --instance-id "$instance_id" --query '{out:StandardOutputContent,err:StandardErrorContent}' --output json >&2
        return 1
        ;;
    esac
    sleep 5
  done
  return 1
}

echo "=== Phase A shadow small (no live cutover) ==="
echo "live   : ${LIVE_EDGE_ID} ${LIVE_NAME} bundle=${live_bundle} state=${live_state} ip=${live_ip} mi=${live_mi}"
echo "shadow : ${SHADOW_NAME} bundle=small_3_0 ip_name=${SHADOW_IP_NAME} tag=${SHADOW_EDGE_TAG}"
echo "guard  : will not write ${LIVE_PREFIX}/* and will not attach ${LIVE_IP_NAME}"
echo

[[ "$live_state" == "running" ]] || { echo "live instance is not running" >&2; exit 1; }
[[ "$live_attached" == "$LIVE_NAME" ]] || { echo "live static IP not on live instance" >&2; exit 1; }
[[ "$live_bundle" == "micro_3_0" ]] || { echo "unexpected live bundle $live_bundle" >&2; exit 1; }
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 12 "https://${DOMAIN}/health" || true)"
[[ "$code" == "200" ]] || { echo "live /health is $code; refusing to start shadow" >&2; exit 1; }

if aws lightsail get-instance --region "$REGION" --instance-name "$SHADOW_NAME" >/dev/null 2>&1; then
  echo "shadow instance already exists; reuse it"
else
  bash "${REPO_ROOT}/deploy/aws/lightsail/render-bootstrap.sh"
  echo "creating SSM hybrid activation tag=EdgeId=${SHADOW_EDGE_TAG}"
  activation_json="$(aws ssm create-activation \
    --region "$REGION" \
    --iam-role "$HYBRID_ROLE" \
    --description "tokenkey lightsail shadow ${SHADOW_EDGE_TAG}" \
    --default-instance-name "$SHADOW_NAME" \
    --registration-limit 1 \
    --tags "Key=Project,Value=tokenkey" "Key=EdgeId,Value=${SHADOW_EDGE_TAG}" "Key=Platform,Value=lightsail")"
  activation_id="$(echo "$activation_json" | jq -r '.ActivationId')"
  activation_code="$(echo "$activation_json" | jq -r '.ActivationCode')"
  [[ -n "$activation_id" && "$activation_id" != "null" ]] || { echo "create-activation failed" >&2; exit 1; }
  echo "activation_id=${activation_id}"

  launch_env="$(mktemp)"
  user_data="$(mktemp)"
  trap 'rm -f "$launch_env" "$user_data"' RETURN
  cat >"$launch_env" <<EOF
export EDGE_ID='${LIVE_EDGE_ID}'
export INSTANCE_NAME='${SHADOW_NAME}'
export API_DOMAIN='${DOMAIN}'
export ACME_EMAIL='${ACME_EMAIL}'
export MAIN_GATEWAY_ALLOWED_CIDR='${MAIN_GW_CIDR}'
export TOKENKEY_IMAGE='ghcr.io/${GHCR_OWNER}/sub2api:${IMAGE_TAG}'
export GHCR_PULL_USER='${GHCR_OWNER}'
export GHCR_PAT_SSM_NAME=''
export LIGHTSAIL_REGION='${REGION}'
export SSM_ACTIVATION_ID='${activation_id}'
export SSM_ACTIVATION_CODE='${activation_code}'
export ADMIN_EMAIL='admin@${DOMAIN}'
export TZ_VALUE='UTC'
export SWAP_SIZE_GIB='2'
export ALLOW_SECRET_GENERATE='false'
EOF
  { cat "$launch_env"; echo; cat "${REPO_ROOT}/deploy/aws/lightsail/generated-launch-script.sh"; } >"$user_data"
  # Lightsail user-data is capped at 16 KB. Env + launch script exceeds that, so
  # wrap a gzip payload in a tiny decoder stub.
  user_data_gz="$(mktemp)"
  gzip -9n -c "$user_data" | base64 | tr -d '\n' >"$user_data_gz"
  python3 - "$user_data_gz" "$user_data" <<'PY'
import pathlib, sys
b64 = pathlib.Path(sys.argv[1]).read_text(encoding="ascii")
pathlib.Path(sys.argv[2]).write_text(
    "#!/bin/bash\nset -euo pipefail\npython3 - <<'PY'\n"
    "import base64, gzip\n"
    f"open('/tmp/tokenkey-shadow-launch.sh','wb').write(gzip.decompress(base64.b64decode('{b64}')))\n"
    "PY\n"
    "exec bash /tmp/tokenkey-shadow-launch.sh\n",
    encoding="ascii",
)
PY
  payload_bytes="$(wc -c < "$user_data")"
  echo "shadow user-data bytes=${payload_bytes}"
  if [[ "$payload_bytes" -ge 16000 ]]; then
    echo "compressed user-data still exceeds Lightsail 16KB limit (${payload_bytes})" >&2
    rm -f "$user_data_gz"
    exit 1
  fi
  rm -f "$user_data_gz"
  echo "creating ${SHADOW_NAME} small_3_0 in ${AZ}"
  aws lightsail create-instances \
    --region "$REGION" \
    --instance-names "$SHADOW_NAME" \
    --availability-zone "$AZ" \
    --blueprint-id amazon_linux_2023 \
    --bundle-id small_3_0 \
    --user-data "$(cat "$user_data")" >/dev/null
  echo "$activation_id" > "/tmp/${SHADOW_NAME}.activation_id"
fi

echo "waiting for shadow instance running"
deadline=$(( $(date +%s) + 600 ))
while [[ $(date +%s) -lt $deadline ]]; do
  state="$(aws lightsail get-instance --region "$REGION" --instance-name "$SHADOW_NAME" --query 'instance.state.name' --output text 2>/dev/null || echo pending)"
  echo "shadow state=${state}"
  [[ "$state" == "running" ]] && break
  sleep 10
done
[[ "$(aws lightsail get-instance --region "$REGION" --instance-name "$SHADOW_NAME" --query 'instance.state.name' --output text)" == "running" ]]

if ! aws lightsail get-static-ip --region "$REGION" --static-ip-name "$SHADOW_IP_NAME" >/dev/null 2>&1; then
  echo "allocating temporary Static IP ${SHADOW_IP_NAME}"
  aws lightsail allocate-static-ip --region "$REGION" --static-ip-name "$SHADOW_IP_NAME" >/dev/null
fi
shadow_attached="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$SHADOW_IP_NAME" --query 'staticIp.attachedTo' --output text)"
if [[ "$shadow_attached" != "$SHADOW_NAME" ]]; then
  aws lightsail attach-static-ip --region "$REGION" --static-ip-name "$SHADOW_IP_NAME" --instance-name "$SHADOW_NAME" >/dev/null
fi
aws lightsail put-instance-public-ports --region "$REGION" --instance-name "$SHADOW_NAME" \
  --port-infos \
    "fromPort=443,toPort=443,protocol=tcp,cidrs=0.0.0.0/0" \
    "fromPort=8443,toPort=8443,protocol=tcp,cidrs=0.0.0.0/0" >/dev/null
SHADOW_IP="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$SHADOW_IP_NAME" --query 'staticIp.ipAddress' --output text)"
echo "shadow temp ip=${SHADOW_IP}"
assert_live_untouched

activation_id="${activation_id:-}"
if [[ -z "$activation_id" && -f "/tmp/${SHADOW_NAME}.activation_id" ]]; then
  activation_id="$(cat "/tmp/${SHADOW_NAME}.activation_id")"
fi
echo "waiting for shadow SSM registration"
shadow_mi=""
deadline=$(( $(date +%s) + 900 ))
while [[ $(date +%s) -lt $deadline ]]; do
  if [[ -n "$activation_id" ]]; then
    shadow_mi="$(aws ssm describe-instance-information --region "$REGION" \
      --filters "Key=ActivationIds,Values=${activation_id}" \
      --query 'InstanceInformationList[0].InstanceId' --output text 2>/dev/null || true)"
  fi
  if [[ -z "$shadow_mi" || "$shadow_mi" == "None" || "$shadow_mi" == "null" ]]; then
    shadow_mi="$(aws ssm describe-instance-information --region "$REGION" \
      --filters "Key=tag:EdgeId,Values=${SHADOW_EDGE_TAG}" \
      --query 'InstanceInformationList[0].InstanceId' --output text 2>/dev/null || true)"
  fi
  if [[ -n "$shadow_mi" && "$shadow_mi" != "None" && "$shadow_mi" != "null" ]]; then
    break
  fi
  echo "shadow mi pending"
  sleep 15
done
[[ -n "$shadow_mi" && "$shadow_mi" != "None" && "$shadow_mi" != "null" ]] || {
  echo "shadow SSM registration timed out" >&2
  exit 1
}
[[ "$shadow_mi" != "$live_mi" ]] || { echo "shadow mi collided with live mi" >&2; exit 1; }
echo "shadow_mi=${shadow_mi}"

echo "waiting for shadow stack (postgres + settings)"
stack_script="$(mktemp)"
cat >"$stack_script" <<'EOF'
docker ps --filter name=tokenkey-postgres --filter health=healthy --format '{{.Names}}' | grep -qx tokenkey-postgres && \
docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tAc "SELECT to_regclass('public.settings')" 2>/dev/null | grep -qx settings && \
echo STACK_READY
EOF
stack_ready=0
deadline=$(( $(date +%s) + 720 ))
while [[ $(date +%s) -lt $deadline ]]; do
  if out="$(ssm_out "$shadow_mi" "shadow-stack-ready" "$stack_script" 2>/dev/null || true)"; then
    if grep -q STACK_READY <<<"$out"; then stack_ready=1; break; fi
  fi
  echo "shadow stack not ready yet"
  sleep 20
done
rm -f "$stack_script"
[[ "$stack_ready" -eq 1 ]] || { echo "shadow stack did not become ready" >&2; exit 1; }
assert_live_untouched

echo "online pg_dump on live (does not stop live)"
dump_script="$(mktemp)"
cat >"$dump_script" <<'EOF'
set -euo pipefail
/usr/local/bin/tokenkey-pgdump.sh >/tmp/pgdump.out
DUMP_PATH="$(ls -1t /var/lib/tokenkey/pgdump/tokenkey-*.sql.gz | head -1)"
test -n "$DUMP_PATH"
test "$(wc -c < "$DUMP_PATH")" -gt 2048
echo "DUMP_PATH=${DUMP_PATH}"
cat /tmp/pgdump.out
EOF
DUMP_PATH="$(ssm_out "$live_mi" "live-pgdump" "$dump_script" | awk -F= '/^DUMP_PATH=/{print $2; exit}')"
rm -f "$dump_script"
DUMP_NAME="$(basename "$DUMP_PATH")"
if [[ -z "$DUMP_NAME" || "$DUMP_NAME" == "pgdump" ]]; then
  echo "could not parse live dump name (got '$DUMP_PATH')" >&2
  exit 1
fi
echo "live dump=${DUMP_NAME}"
count_sql="SELECT string_agg(n || '=' || c, ',') FROM ( "
first=1
for t in "${PRECIOUS_TABLES[@]}"; do
  if [[ "$first" -eq 1 ]]; then first=0; else count_sql+=" UNION ALL "; fi
  count_sql+="SELECT '${t}' AS n, count(*)::text AS c FROM ${t}"
done
count_sql+=" ) s;"
count_script="$(mktemp)"
cat >"$count_script" <<EOF
docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tAc "${count_sql}"
EOF
DUMP_COUNTS="$(ssm_out "$live_mi" "live-precious-counts-at-dump" "$count_script" | tr -d '[:space:]')"
echo "dump_counts ${DUMP_COUNTS}"
assert_live_untouched

echo "restore dump + copy caddy data onto shadow"
seed_script="$(mktemp)"
cat >"$seed_script" <<EOF
set -euo pipefail
install -d -m 0755 /var/lib/tokenkey/pgdump
aws s3 cp --only-show-errors "${DUMP_S3}/${DUMP_NAME}" /var/lib/tokenkey/pgdump/restore.sql.gz
test "\$(wc -c < /var/lib/tokenkey/pgdump/restore.sql.gz)" -gt 2048
docker stop tokenkey
docker exec tokenkey-postgres psql -U tokenkey -d postgres -v ON_ERROR_STOP=1 -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='tokenkey' AND pid <> pg_backend_pid();" >/dev/null
docker exec tokenkey-postgres psql -U tokenkey -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE tokenkey;"
docker exec tokenkey-postgres psql -U tokenkey -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE tokenkey OWNER tokenkey;"
gunzip -c /var/lib/tokenkey/pgdump/restore.sql.gz | docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 >/tmp/restore.log
docker start tokenkey
if ! grep -q '^TOKENKEY_PGDUMP_S3_URI=' /var/lib/tokenkey/.env; then
  echo 'TOKENKEY_PGDUMP_S3_URI=${DUMP_S3}' >> /var/lib/tokenkey/.env
fi
echo RESTORE_OK
EOF
ssm_run "$shadow_mi" "shadow-restore-pg" "$seed_script"
rm -f "$seed_script"

caddy_pack="$(mktemp)"
cat >"$caddy_pack" <<EOF
set -euo pipefail
tar -C /var/lib/tokenkey/caddy -czf /tmp/caddy-data.tgz data
aws s3 cp --only-show-errors /tmp/caddy-data.tgz "${DUMP_S3}/shadow-xfer-caddy-data.tgz"
rm -f /tmp/caddy-data.tgz
echo CADDY_PACKED
EOF
ssm_run "$live_mi" "live-pack-caddy" "$caddy_pack"
rm -f "$caddy_pack"
assert_live_untouched

caddy_unpack="$(mktemp)"
cat >"$caddy_unpack" <<EOF
set -euo pipefail
aws s3 cp --only-show-errors "${DUMP_S3}/shadow-xfer-caddy-data.tgz" /tmp/caddy-data.tgz
docker stop tokenkey-caddy
rm -rf /var/lib/tokenkey/caddy/data
tar -C /var/lib/tokenkey/caddy -xzf /tmp/caddy-data.tgz
rm -f /tmp/caddy-data.tgz
docker start tokenkey-caddy
echo CADDY_RESTORED
EOF
ssm_run "$shadow_mi" "shadow-unpack-caddy" "$caddy_unpack"
rm -f "$caddy_unpack"

echo "waiting for shadow app healthy"
app_script="$(mktemp)"
cat >"$app_script" <<'EOF'
docker ps --filter name=^/tokenkey$ --filter health=healthy --format '{{.Names}}' | grep -qx tokenkey && echo APP_READY
EOF
deadline=$(( $(date +%s) + 240 ))
app_ready=0
while [[ $(date +%s) -lt $deadline ]]; do
  if out="$(ssm_out "$shadow_mi" "shadow-app-ready" "$app_script" 2>/dev/null || true)"; then
    if grep -q APP_READY <<<"$out"; then app_ready=1; break; fi
  fi
  sleep 10
done
rm -f "$app_script"
[[ "$app_ready" -eq 1 ]] || { echo "shadow app not healthy" >&2; exit 1; }

echo "compare precious table counts to dump-time live snapshot"
SHADOW_COUNTS="$(ssm_out "$shadow_mi" "shadow-precious-counts" "$count_script" | tr -d '[:space:]')"
rm -f "$count_script"
echo "dump_counts   ${DUMP_COUNTS}"
echo "shadow_counts ${SHADOW_COUNTS}"
python3 - "$DUMP_COUNTS" "$SHADOW_COUNTS" <<'PY'
import sys
def parse(raw: str) -> dict[str, int]:
    out = {}
    for part in raw.split(","):
        if not part or "=" not in part:
            raise SystemExit(f"bad count blob: {raw!r}")
        k, v = part.split("=", 1)
        out[k] = int(v)
    return out
dump, shadow = parse(sys.argv[1]), parse(sys.argv[2])
identity = ("users", "accounts", "api_keys", "groups", "settings")
for key in identity:
    if dump.get(key) != shadow.get(key):
        raise SystemExit(f"identity table {key} dump={dump.get(key)} shadow={shadow.get(key)}")
delta = abs(dump.get("usage_billing_dedup", -1) - shadow.get("usage_billing_dedup", -2))
if delta > 20:
    raise SystemExit(f"usage_billing_dedup drift {delta} exceeds slack 20")
print(f"precious identity match; billing_dedup_delta={delta}")
PY

mem_script="$(mktemp)"
cat >"$mem_script" <<'EOF'
awk '/MemAvailable:/ {print $2}' /proc/meminfo
free -m | awk 'NR==2{print "mem_total_mb="$2}'
EOF
SHADOW_MEM_KB="$(ssm_out "$shadow_mi" "shadow-mem" "$mem_script" | head -1 | tr -d '[:space:]')"
rm -f "$mem_script"
echo "shadow MemAvailable_kB=${SHADOW_MEM_KB}"
[[ "$SHADOW_MEM_KB" -gt 327680 ]] || { echo "shadow MemAvailable below 320MiB" >&2; exit 1; }

echo "smoke shadow via temp IP + live SNI"
shadow_code="000"
for _ in $(seq 1 20); do
  shadow_code="$(curl -sS -o /tmp/shadow-health -w '%{http_code}' --max-time 15 \
    --resolve "${DOMAIN}:443:${SHADOW_IP}" "https://${DOMAIN}/health" || true)"
  echo "shadow health via ${SHADOW_IP} -> ${shadow_code}"
  [[ "$shadow_code" == "200" ]] && break
  sleep 6
done
[[ "$shadow_code" == "200" ]] || { echo "shadow /health failed" >&2; exit 1; }
grep -q '"status":"ok"' /tmp/shadow-health

live_code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 12 "https://${DOMAIN}/health" || true)"
[[ "$live_code" == "200" ]] || { echo "live /health broke during Phase A: $live_code" >&2; exit 1; }
assert_live_untouched

echo
echo "PHASE_A_OK edge=${LIVE_EDGE_ID} shadow=${SHADOW_NAME} shadow_ip=${SHADOW_IP} shadow_mi=${shadow_mi} dump=${DUMP_NAME}"
echo "live still ${LIVE_NAME} ip=${live_ip} mi=${live_mi} /health=200"
echo "do not start Phase B until scheduled"
