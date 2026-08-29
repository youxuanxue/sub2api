#!/usr/bin/env bash
# Phase B: final dump → restore sidecar → move the live Static IP.
# Old instance stays running for rollback. Compose on the old host is stopped
# only after the new host serves https://<domain>/health.
#
# Usage:
#   bash ops/lightsail/cutover-shadow-small.sh us3
set -euo pipefail

LIVE_EDGE_ID="${1:?usage: $0 <live_edge_id>}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PRECIOUS_TABLES=(users accounts api_keys groups settings)

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }
TARGET_JSON="$(python3 "${REPO_ROOT}/deploy/aws/lightsail/resolve-edge-lightsail-target.py" --edge-id "$LIVE_EDGE_ID")"
field() { printf '%s\n' "$TARGET_JSON" | awk -F= -v k="$1" '$1==k {print substr($0, index($0,"=")+1); exit}'; }

REGION="$(field lightsail_region)"
LIVE_NAME="$(field instance_name)"
if [[ "$LIVE_NAME" == *-s30 ]]; then
  echo "matrix instance_name=${LIVE_NAME} already points at the small sidecar; refuse to derive ${LIVE_NAME}-s30" >&2
  exit 1
fi
LIVE_IP_NAME="$(field static_ip_name)"
DOMAIN="$(field domain)"
LIVE_PREFIX="$(field ssm_prefix)"
SHADOW_NAME="${LIVE_NAME}-s30"
SHADOW_IP_NAME="${LIVE_NAME}-s30-ip"
DUMP_S3="s3://tokenkey-prod-pgdump-682751977094/edge/${LIVE_EDGE_ID}/pgdump"

live_ip="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" --query 'staticIp.ipAddress' --output text)"
live_attached="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" --query 'staticIp.attachedTo' --output text)"
live_mi="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/ssm_managed_instance_id" --query 'Parameter.Value' --output text)"
live_param_name="$(aws ssm get-parameter --region "$REGION" --name "${LIVE_PREFIX}/instance_name" --query 'Parameter.Value' --output text)"
shadow_mi="$(aws ssm describe-instance-information --region "$REGION" \
  --filters "Key=tag:EdgeId,Values=${LIVE_EDGE_ID}-s30" \
  --query 'InstanceInformationList[0].InstanceId' --output text)"
shadow_bundle="$(aws lightsail get-instance --region "$REGION" --instance-name "$SHADOW_NAME" --query 'instance.bundleId' --output text)"
shadow_state="$(aws lightsail get-instance --region "$REGION" --instance-name "$SHADOW_NAME" --query 'instance.state.name' --output text)"

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

rollback_ip() {
  echo "::error::Phase B health failed; moving Static IP back to $LIVE_NAME"
  aws lightsail detach-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" >/dev/null 2>&1 || true
  aws lightsail attach-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" \
    --instance-name "$LIVE_NAME" >/dev/null 2>&1 || true
}

echo "=== Phase B cutover (final dump + Static IP move) ==="
echo "live   : ${LIVE_NAME} ip=${live_ip} attachedTo=${live_attached} mi=${live_mi}"
echo "shadow : ${SHADOW_NAME} bundle=${shadow_bundle} state=${shadow_state} mi=${shadow_mi}"
echo

[[ "$live_attached" == "$LIVE_NAME" ]] || { echo "prod IP is not on live instance" >&2; exit 1; }
[[ "$live_param_name" == "$LIVE_NAME" ]] || { echo "SSM instance_name already moved; abort" >&2; exit 1; }
[[ "$shadow_bundle" == "small_3_0" && "$shadow_state" == "running" ]] || { echo "shadow is not a running small_3_0" >&2; exit 1; }
[[ "$shadow_mi" != "None" && "$shadow_mi" != "$live_mi" ]] || { echo "shadow mi missing or collided" >&2; exit 1; }
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 12 "https://${DOMAIN}/health" || true)"
[[ "$code" == "200" ]] || { echo "live /health is $code; refusing cutover" >&2; exit 1; }

echo "final online pg_dump on live"
dump_script="$(mktemp)"
cat >"$dump_script" <<'EOF'
set -euo pipefail
/usr/local/bin/tokenkey-pgdump.sh >/tmp/pgdump.out
DUMP_PATH="$(ls -1t /var/lib/tokenkey/pgdump/tokenkey-*.sql.gz | head -1)"
test -n "$DUMP_PATH"
test "$(wc -c < "$DUMP_PATH")" -gt 2048
echo "DUMP_PATH=${DUMP_PATH}"
EOF
DUMP_PATH="$(ssm_out "$live_mi" "b-live-pgdump" "$dump_script" | awk -F= '/^DUMP_PATH=/{print $2; exit}')"
rm -f "$dump_script"
DUMP_NAME="$(basename "$DUMP_PATH")"
[[ -n "$DUMP_NAME" && "$DUMP_NAME" != "pgdump" ]] || { echo "could not parse dump name" >&2; exit 1; }
echo "final dump=${DUMP_NAME}"

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
DUMP_COUNTS="$(ssm_out "$live_mi" "b-live-counts" "$count_script" | tr -d '[:space:]')"
echo "dump_counts ${DUMP_COUNTS}"

echo "restore dump + caddy onto shadow"
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
echo RESTORE_OK
EOF
ssm_run "$shadow_mi" "b-shadow-restore-pg" "$seed_script"
rm -f "$seed_script"

caddy_pack="$(mktemp)"
cat >"$caddy_pack" <<EOF
set -euo pipefail
tar -C /var/lib/tokenkey/caddy -czf /tmp/caddy-data.tgz data
aws s3 cp --only-show-errors /tmp/caddy-data.tgz "${DUMP_S3}/shadow-xfer-caddy-data.tgz"
rm -f /tmp/caddy-data.tgz
echo CADDY_PACKED
EOF
ssm_run "$live_mi" "b-live-pack-caddy" "$caddy_pack"
rm -f "$caddy_pack"

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
ssm_run "$shadow_mi" "b-shadow-unpack-caddy" "$caddy_unpack"
rm -f "$caddy_unpack"

app_script="$(mktemp)"
cat >"$app_script" <<'EOF'
docker ps --filter name=^/tokenkey$ --filter health=healthy --format '{{.Names}}' | grep -qx tokenkey && echo APP_READY
EOF
deadline=$(( $(date +%s) + 240 ))
app_ready=0
while [[ $(date +%s) -lt $deadline ]]; do
  if out="$(ssm_out "$shadow_mi" "b-shadow-app-ready" "$app_script" 2>/dev/null || true)"; then
    if grep -q APP_READY <<<"$out"; then app_ready=1; break; fi
  fi
  sleep 8
done
rm -f "$app_script"
[[ "$app_ready" -eq 1 ]] || { echo "shadow app not healthy after restore" >&2; exit 1; }

SHADOW_COUNTS="$(ssm_out "$shadow_mi" "b-shadow-counts" "$count_script" | tr -d '[:space:]')"
rm -f "$count_script"
echo "shadow_counts ${SHADOW_COUNTS}"
python3 "${REPO_ROOT}/ops/lightsail/shadow_count_compare.py" "$DUMP_COUNTS" "$SHADOW_COUNTS"

echo "pre-cutover live /health still required"
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 12 "https://${DOMAIN}/health" || true)"
[[ "$code" == "200" ]] || { echo "live dropped before IP move: $code" >&2; exit 1; }

echo "moving production Static IP ${LIVE_IP_NAME}=${live_ip} -> ${SHADOW_NAME}"
aws lightsail detach-static-ip --region "$REGION" --static-ip-name "$SHADOW_IP_NAME" >/dev/null || true
aws lightsail detach-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" >/dev/null
sleep 3
aws lightsail attach-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" \
  --instance-name "$SHADOW_NAME" >/dev/null
attached_now="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$LIVE_IP_NAME" --query 'staticIp.attachedTo' --output text)"
if [[ "$attached_now" != "$SHADOW_NAME" ]]; then
  echo "prod IP attach missed shadow (attachedTo=$attached_now)" >&2
  rollback_ip
  exit 1
fi

echo "waiting for https://${DOMAIN}/health on moved IP"
health_ok=0
for _ in $(seq 1 30); do
  code="$(curl -sS -o /tmp/phaseb-health -w '%{http_code}' --max-time 12 "https://${DOMAIN}/health" || true)"
  echo "cutover health -> ${code}"
  if [[ "$code" == "200" ]] && grep -q '"status":"ok"' /tmp/phaseb-health; then
    health_ok=1
    break
  fi
  sleep 4
done
if [[ "$health_ok" -ne 1 ]]; then
  rollback_ip
  exit 1
fi

echo "updating live SSM identity to shadow (IP address unchanged)"
aws ssm put-parameter --region "$REGION" --name "${LIVE_PREFIX}/instance_name" --type String --value "$SHADOW_NAME" --overwrite >/dev/null
aws ssm put-parameter --region "$REGION" --name "${LIVE_PREFIX}/ssm_managed_instance_id" --type String --value "$shadow_mi" --overwrite >/dev/null
aws ssm put-parameter --region "$REGION" --name "${LIVE_PREFIX}/public_ip" --type String --value "$live_ip" --overwrite >/dev/null
aws ssm put-parameter --region "$REGION" --name "${LIVE_PREFIX}/static_ip_name" --type String --value "$LIVE_IP_NAME" --overwrite >/dev/null

echo "stopping compose on old instance to avoid dual OAuth; instance stays running"
stop_old="$(mktemp)"
cat >"$stop_old" <<'EOF'
set -euo pipefail
cd /var/lib/tokenkey
docker compose --env-file .env stop tokenkey tokenkey-caddy || true
echo OLD_STACK_STOPPED
EOF
ssm_run "$live_mi" "b-stop-old-app" "$stop_old" || echo "warning: could not stop old compose; old instance still has no prod IP"
rm -f "$stop_old"

echo
echo "PHASE_B_OK edge=${LIVE_EDGE_ID} serving=${SHADOW_NAME} bundle=small_3_0 ip=${live_ip} mi=${shadow_mi} dump=${DUMP_NAME}"
echo "old instance ${LIVE_NAME} remains running without prod IP for 2h rollback"
echo "update matrix instance_name=${SHADOW_NAME} bundle_id=small_3_0"
