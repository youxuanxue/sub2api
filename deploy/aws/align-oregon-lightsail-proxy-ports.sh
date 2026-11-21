#!/usr/bin/env bash
# Align Oregon Lightsail SOCKS egress firewall with fleet policy:
#   - :1080 only from prod app public IPs (app-old + app-cand)
#   - no public 22 / 80 / 443 / 8443 (manage via Lightsail console / temporary open)
#
# Usage:
#   bash deploy/aws/align-oregon-lightsail-proxy-ports.sh --check
#   bash deploy/aws/align-oregon-lightsail-proxy-ports.sh --apply
#
# Region fixed to us-west-2. Instance set is the live or-1..5 inventory
# (deploy/hetzner/README.md Oregon section).
set -euo pipefail

REGION=us-west-2
# Keep in sync with deploy/hetzner/README.md Oregon table + prod app public IPs.
INSTANCES=(
  tokenkey-ls-or-01
  tokenkey-ls-or-02
  tokenkey-ls-or-03
  tokenkey-ls-or-04
  tokenkey-ls-or-claude
)
ALLOWED_1080_CIDRS=(
  43.162.83.233/32
  43.162.126.44/32
)
FORBIDDEN_PORTS=(
  "22:tcp"
  "80:tcp"
  "443:tcp"
  "443:udp"
  "8443:tcp"
)

MODE="${1:-}"
case "$MODE" in
  --check|--apply) ;;
  *)
    echo "usage: $0 --check|--apply" >&2
    exit 2
    ;;
esac

command -v aws >/dev/null || { echo "aws cli required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 required" >&2; exit 1; }

close_port() {
  local name="$1" from="$2" proto="$3"
  aws lightsail close-instance-public-ports \
    --region "$REGION" \
    --instance-name "$name" \
    --port-info "fromPort=$from,toPort=$from,protocol=$proto" \
    --output json >/dev/null 2>&1 || true
}

ensure_1080() {
  local name="$1"
  # Replace 1080 with the exact allowlist (open is additive; close then open).
  close_port "$name" 1080 tcp
  local cidr_csv
  cidr_csv=$(IFS=,; echo "${ALLOWED_1080_CIDRS[*]}")
  aws lightsail open-instance-public-ports \
    --region "$REGION" \
    --instance-name "$name" \
    --port-info "fromPort=1080,toPort=1080,protocol=tcp,cidrs=$cidr_csv" \
    --output json >/dev/null
}

evaluate_ports() {
  local name="$1"
  local payload allowed_json forbidden_json
  payload="$(aws lightsail get-instance --region "$REGION" --instance-name "$name" --output json)"
  allowed_json=$(printf '%s\n' "${ALLOWED_1080_CIDRS[@]}" | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))')
  forbidden_json=$(printf '%s\n' "${FORBIDDEN_PORTS[@]}" | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))')
  INSTANCE_JSON="$payload" ALLOWED_JSON="$allowed_json" FORBIDDEN_JSON="$forbidden_json" python3 - <<'PY'
import json, os
raw = json.loads(os.environ["INSTANCE_JSON"])["instance"]
allowed = set(json.loads(os.environ["ALLOWED_JSON"]))
forbidden = set(json.loads(os.environ["FORBIDDEN_JSON"]))
ports = (raw.get("networking") or {}).get("ports") or []
findings = []
have_1080 = False
for p in ports:
    key = f"{p.get('fromPort')}:{p.get('protocol')}"
    cidrs = set(p.get("cidrs") or [])
    if p.get("fromPort") == 1080 and p.get("protocol") == "tcp":
        have_1080 = True
        if cidrs != allowed:
            findings.append(f"1080 cidrs={sorted(cidrs)} want={sorted(allowed)}")
        continue
    if key in forbidden or "0.0.0.0/0" in cidrs:
        findings.append(f"public {key} cidrs={sorted(cidrs)}")
if not have_1080:
    findings.append("missing 1080 allowlist")
print(json.dumps({
    "name": raw.get("name"),
    "ip": raw.get("publicIpAddress"),
    "ok": not findings,
    "findings": findings,
}, sort_keys=True))
PY
}

fail=0
for name in "${INSTANCES[@]}"; do
  if [[ "$MODE" == "--apply" ]]; then
    for spec in "${FORBIDDEN_PORTS[@]}"; do
      from="${spec%%:*}"
      proto="${spec##*:}"
      close_port "$name" "$from" "$proto"
    done
    ensure_1080 "$name"
  fi
  result="$(evaluate_ports "$name")"
  echo "$result"
  if ! echo "$result" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("ok") else 1)'; then
    fail=1
  fi
done
exit "$fail"
