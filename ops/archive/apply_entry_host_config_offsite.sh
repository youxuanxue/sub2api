#!/usr/bin/env bash
# Orchestrate plaintext entry host-config → COS (prod).
# 1) optional: open entry Lighthouse FW TCP/8765 from data private IP
# 2) entry: pack + one-shot private HTTP
# 3) data: curl + upload_host_config --apply
# 4) remove the temp FW rule
#
#   TK_HOST_CONFIG_APPLY=1 bash ops/archive/apply_entry_host_config_offsite.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROBE="$ROOT/ops/observability/run-probe.sh"
MATRIX="$ROOT/deploy/tencent/candidate-usw-20261022.json"
PORT="${TK_HOST_CONFIG_PORT:-8765}"

if [ "${TK_HOST_CONFIG_APPLY:-}" != "1" ]; then
  echo "REFUSED: set TK_HOST_CONFIG_APPLY=1 to upload plaintext entry certs to COS" >&2
  exit 64
fi

read -r ENTRY_LHINS DATA_IP < <(python3 - "$MATRIX" <<'PY'
import json, sys
m = json.load(open(sys.argv[1], encoding="utf-8"))
print(m["entry"]["lighthouse_instance_id"], m["data_machine"]["private_ip"])
PY
)

cleanup_fw() {
  if [ "${FW_OPENED:-0}" = "1" ]; then
    echo "== cleanup firewall $ENTRY_LHINS tcp/$PORT from ${DATA_IP}/32" >&2
    tccli lighthouse DeleteFirewallRules --region na-siliconvalley \
      --InstanceId "$ENTRY_LHINS" \
      --FirewallRules "[{\"Protocol\":\"TCP\",\"Port\":\"$PORT\",\"CidrBlock\":\"${DATA_IP}/32\",\"Action\":\"ACCEPT\",\"FirewallRuleDescription\":\"temp host-config pull from data\"}]" \
      >/dev/null || true
  fi
}
trap cleanup_fw EXIT

echo "== 0/3 open firewall tcp/$PORT ${DATA_IP}/32 -> $ENTRY_LHINS" >&2
FW_JSON="[{\"Protocol\":\"TCP\",\"Port\":\"$PORT\",\"CidrBlock\":\"${DATA_IP}/32\",\"Action\":\"ACCEPT\",\"FirewallRuleDescription\":\"temp host-config pull from data\"}]"
if tccli lighthouse CreateFirewallRules --region na-siliconvalley \
  --InstanceId "$ENTRY_LHINS" \
  --FirewallRules "$FW_JSON" >/tmp/tk-hc-fw-create.json 2>/tmp/tk-hc-fw-create.err; then
  FW_OPENED=1
elif grep -q 'FirewallRulesExist' /tmp/tk-hc-fw-create.err; then
  echo "  (rule already present; will still attempt cleanup)" >&2
  FW_OPENED=1
else
  cat /tmp/tk-hc-fw-create.err >&2
  exit 2
fi

echo "== 1/3 entry pack+serve" >&2
SERVE_JSON="$(
  bash "$PROBE" \
    --target prod --node entry \
    --script "$ROOT/ops/archive/probe-entry-host-config-pack-serve.sh" \
    --env TK_HOST_CONFIG_SERVE=1 \
    --env "TK_HOST_CONFIG_PORT=$PORT"
)"
echo "$SERVE_JSON" >&2
URL="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["url"])' <<<"$SERVE_JSON")"
SHA="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["sha256"])' <<<"$SERVE_JSON")"
[ -n "$URL" ] && [ -n "$SHA" ]

echo "== 2/3 data fetch+upload" >&2
UPLOAD_JSON="$(
  bash "$PROBE" \
    --target prod-data \
    --script "$ROOT/ops/archive/probe-data-fetch-upload-entry-host-config.sh" \
    --env TK_HOST_CONFIG_APPLY=1 \
    --env "TK_HOST_CONFIG_URL=$URL" \
    --env "TK_HOST_CONFIG_SHA256=$SHA" \
    --env TK_HOST_CONFIG_LABEL=prod-entry
)"
echo "$UPLOAD_JSON"
python3 -c 'import json,sys; d=json.load(sys.stdin); assert d.get("ok") is True; assert d["receipt"]["operation"]=="apply"' <<<"$UPLOAD_JSON"
echo "RESULT ok plaintext host-config uploaded" >&2
