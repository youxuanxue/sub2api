#!/usr/bin/env bash
# Orchestrate plaintext entry host-config → COS (prod).
# 1) open entry Lighthouse FW TCP/8765 from data private IP
# 2) entry: pack + one-shot private HTTP
# 3) data: curl + upload_host_config --apply
# 4) remove matching temp FW rule(s) by Port+Cidr (description-agnostic)
#
#   TK_HOST_CONFIG_APPLY=1 bash ops/archive/apply_entry_host_config_offsite.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROBE="$ROOT/ops/observability/run-probe.sh"
MATRIX="$ROOT/deploy/tencent/candidate-usw-20261022.json"
PORT="${TK_HOST_CONFIG_PORT:-8765}"
REGION=na-siliconvalley
FW_DESC="temp host-config pull from data"

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

# Delete every ACCEPT TCP/$PORT rule from ${DATA_IP}/32 regardless of description.
# Description mismatch previously left a dangling 8765 hole when Create hit Exists.
delete_matching_fw() {
  local raw matches
  raw="$(tccli lighthouse DescribeFirewallRules --region "$REGION" \
    --InstanceId "$ENTRY_LHINS" --output json)"
  matches="$(python3 - "$raw" "$PORT" "${DATA_IP}/32" <<'PY'
import json, sys
doc = json.loads(sys.argv[1])
port, cidr = sys.argv[2], sys.argv[3]
out = []
for rule in doc.get("FirewallRuleSet") or []:
    if (
        str(rule.get("Protocol") or "").upper() == "TCP"
        and str(rule.get("Port") or "") == port
        and str(rule.get("CidrBlock") or "") == cidr
        and str(rule.get("Action") or "").upper() == "ACCEPT"
    ):
        out.append({
            "Protocol": "TCP",
            "Port": port,
            "CidrBlock": cidr,
            "Action": "ACCEPT",
            "FirewallRuleDescription": rule.get("FirewallRuleDescription") or "",
        })
print(json.dumps(out, ensure_ascii=False))
PY
)"
  if [ "$matches" = "[]" ]; then
    echo "  no matching firewall rules to delete" >&2
    return 0
  fi
  echo "  deleting: $matches" >&2
  tccli lighthouse DeleteFirewallRules --region "$REGION" \
    --InstanceId "$ENTRY_LHINS" \
    --FirewallRules "$matches" >/dev/null
}

cleanup_fw() {
  if [ "${FW_OPENED:-0}" != "1" ]; then
    return 0
  fi
  echo "== cleanup firewall $ENTRY_LHINS tcp/$PORT from ${DATA_IP}/32" >&2
  if ! delete_matching_fw; then
    echo "WARN: firewall cleanup failed; check DescribeFirewallRules on $ENTRY_LHINS" >&2
  fi
}
trap cleanup_fw EXIT

echo "== 0/3 open firewall tcp/$PORT ${DATA_IP}/32 -> $ENTRY_LHINS" >&2
FW_JSON="$(python3 -c 'import json,sys; print(json.dumps([{"Protocol":"TCP","Port":sys.argv[1],"CidrBlock":sys.argv[2]+"/32","Action":"ACCEPT","FirewallRuleDescription":sys.argv[3]}]))' "$PORT" "$DATA_IP" "$FW_DESC")"
if tccli lighthouse CreateFirewallRules --region "$REGION" \
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
