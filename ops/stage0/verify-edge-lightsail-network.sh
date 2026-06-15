#!/usr/bin/env bash
# verify-edge-lightsail-network.sh — Post-provision / post-DNS checks for Lightsail edges.
#
# Hardened public-port baseline: TCP 443 + 8443 OPEN, TCP 80 and 22 CLOSED.
# 443 carries business + ACME TLS-ALPN-01 (Caddyfile.edge disable_http_challenge),
# so HTTP-01 / public 80 is unneeded; 8443 is an alternate connection port kept
# open by design (NOT force-closed); SSH (22) is operated via SSM / Lightsail
# console, never the public port. Cert renewal still rides 443 only.
#
# Confirms the firewall matches that baseline and (optionally) public HTTPS
# /health succeeds. --enforce-ports rewrites the firewall to 443 + 8443 (closes
# 22/80, keeps 8443); --renew-cert restarts Caddy when DNS was late (ACME NXDOMAIN).
#
# Usage:
#   bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> [--enforce-ports] [--renew-cert]
#
# Exit codes:
#   0 — ports OK (+ health OK when DNS resolves to static IP)
#   1 — usage / matrix error
#   2 — AWS or curl failure after remediation attempts
#   3 — 443 still not open after remediation (permissions?)

set -euo pipefail

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_OPS_DIR}/../.." && pwd)"
RESOLVE="${REPO_ROOT}/deploy/aws/lightsail/resolve-edge-lightsail-target.py"

ENFORCE_PORTS=false
RENEW_CERT=false
EDGE_ID=""

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/verify-edge-lightsail-network.sh <edge_id> [--enforce-ports] [--renew-cert]

Hardened baseline: TCP 443 + 8443 open; TCP 80 and 22 closed.

Checks:
  - Lightsail public port 443 is open (80/22 expected closed; drift warned)
  - Lightsail public port 8443 is open (alternate connection port; warn if missing)
  - dig @1.1.1.1 A record matches static IP (when DNS exists)
  - curl https://api-<edge_id>.tokenkey.dev/health (best effort)

Options:
  --enforce-ports  put-instance-public-ports to 443 + 8443 (closes public 22 and 80)
  --renew-cert     restart tokenkey-caddy via SSM (after DNS cutover / ACME retry)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --enforce-ports)
      ENFORCE_PORTS=true
      shift
      ;;
    --renew-cert)
      RENEW_CERT=true
      shift
      ;;
    -*)
      echo "[verify-edge-lightsail-network] ERROR: unknown flag: $1" >&2
      usage >&2
      exit 1
      ;;
    *)
      if [[ -z "$EDGE_ID" ]]; then
        EDGE_ID="${1#edge-}"
      else
        echo "[verify-edge-lightsail-network] ERROR: unexpected arg: $1" >&2
        exit 1
      fi
      shift
      ;;
  esac
done

if [[ -z "$EDGE_ID" ]]; then
  usage >&2
  exit 1
fi

if [[ ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[verify-edge-lightsail-network] ERROR: invalid edge id: $EDGE_ID" >&2
  exit 1
fi

for tool in aws jq python3 curl dig; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "[verify-edge-lightsail-network] ERROR: required tool missing: $tool" >&2
    exit 2
  fi
done

_resolve_kv() {
  python3 "$RESOLVE" --edge-id "$EDGE_ID" | awk -F= -v k="$1" '$1==k {print $2; exit}'
}

lightsail_region="$(_resolve_kv lightsail_region)"
instance_name="$(_resolve_kv instance_name)"
domain="$(_resolve_kv domain)"
static_ip_name="$(_resolve_kv static_ip_name)"

REGION="${lightsail_region:?}"
INSTANCE="${instance_name:?}"
DOMAIN="${domain:?}"
STATIC_IP_NAME="${static_ip_name:?}"

STATIC_IP="$(aws lightsail get-static-ip --region "$REGION" --static-ip-name "$STATIC_IP_NAME" \
  --query 'staticIp.ipAddress' --output text 2>/dev/null || echo "")"
if [[ -z "$STATIC_IP" || "$STATIC_IP" == "None" ]]; then
  echo "[verify-edge-lightsail-network] ERROR: could not resolve static IP for ${STATIC_IP_NAME}" >&2
  exit 2
fi

port_open() {
  local port="$1"
  aws lightsail get-instance-port-states --region "$REGION" --instance-name "$INSTANCE" \
    --query "portStates[?fromPort==\`${port}\` && toPort==\`${port}\` && state=='open'] | length(@)" \
    --output text 2>/dev/null | grep -qv '^0$'
}

echo "[verify-edge-lightsail-network] edge_id=${EDGE_ID} region=${REGION} instance=${INSTANCE}"
echo "[verify-edge-lightsail-network] domain=${DOMAIN} static_ip=${STATIC_IP}"

# Hardened baseline = 443 + 8443 open, 80/22 closed. --enforce-ports rewrites the
# whole firewall to exactly that (put = replace semantics, so it also drops the
# Lightsail-default public SSH 22 and any historical 80, while KEEPING 8443).
if $ENFORCE_PORTS; then
  echo "[verify-edge-lightsail-network] enforcing public ports = 443 + 8443 (closes 22 and 80, keeps 8443)..."
  aws lightsail put-instance-public-ports \
    --region "$REGION" \
    --instance-name "$INSTANCE" \
    --port-infos \
      "fromPort=443,toPort=443,protocol=tcp,cidrs=0.0.0.0/0" \
      "fromPort=8443,toPort=8443,protocol=tcp,cidrs=0.0.0.0/0" >/dev/null \
    || { echo "[verify-edge-lightsail-network] FAIL: put-instance-public-ports failed" >&2; exit 3; }
fi

if ! port_open 443; then
  echo "[verify-edge-lightsail-network] FAIL: TCP 443 not open (rerun with --enforce-ports)" >&2
  exit 3
fi
echo "[verify-edge-lightsail-network] ok: firewall TCP 443 open"

# 8443 is part of the hardened baseline (alternate connection port). Open is
# expected; warn (not fail) if missing so an operator can rerun --enforce-ports.
if port_open 8443; then
  echo "[verify-edge-lightsail-network] ok: firewall TCP 8443 open"
else
  echo "[verify-edge-lightsail-network] WARN: public TCP 8443 not open (baseline is 443 + 8443) — run with --enforce-ports to open" >&2
fi

# Drift from the hardened baseline: 80 / 22 should be closed.
drift_ports=()
port_open 80 && drift_ports+=("80")
port_open 22 && drift_ports+=("22")
if [[ ${#drift_ports[@]} -gt 0 ]]; then
  echo "[verify-edge-lightsail-network] WARN: public TCP ${drift_ports[*]} still open (baseline is 443 + 8443) — run with --enforce-ports to close" >&2
fi

if $RENEW_CERT; then
  echo "[verify-edge-lightsail-network] restarting tokenkey-caddy via SSM..."
  PARAM_BODY="$(mktemp)"
  cleanup_param() { rm -f "${PARAM_BODY}"; }
  trap cleanup_param EXIT
  python3 - <<'PY' >"${PARAM_BODY}"
import json, sys
# Single &&-chained command: AWS-RunShellScript joins the `commands` array into
# one script with NO `set -e`, so a separate trailing `sleep 15` would mask a
# failed `docker restart` (the SSM invocation would report success even if Caddy
# never restarted). Chain so a restart failure surfaces as a Failed invocation.
json.dump({"commands": ["sudo docker restart tokenkey-caddy && sleep 15"]}, sys.stdout)
PY
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "verify-edge-lightsail-network renew cert ${EDGE_ID}" \
    --parameters "file://${PARAM_BODY}" \
    --query Command.CommandId --output text)"
  # NB: intentionally NOT ssm_resolve_invocation_mi.inc.sh — this path uses a
  # one-shot --details read + `ssm wait command-executed` rather than the shared
  # 180s poll loop (different control flow, not a duplicate to converge).
  MI="$(aws ssm list-command-invocations --region "$REGION" --command-id "$COMMAND_ID" --details \
    --query 'CommandInvocations[0].InstanceId' --output text)"
  aws ssm wait command-executed --region "$REGION" --command-id "$COMMAND_ID" --instance-id "$MI" || true
  STATUS="$(aws ssm get-command-invocation --region "$REGION" --command-id "$COMMAND_ID" \
    --instance-id "$MI" --query Status --output text)"
  if [[ "$STATUS" != "Success" ]]; then
    echo "[verify-edge-lightsail-network] WARN: caddy restart SSM status=${STATUS}" >&2
  else
    echo "[verify-edge-lightsail-network] ok: tokenkey-caddy restarted"
  fi
fi

DNS_IP="$(dig +short "$DOMAIN" @1.1.1.1 | head -n1 || true)"
if [[ -n "$DNS_IP" && "$DNS_IP" != "$STATIC_IP" ]]; then
  echo "[verify-edge-lightsail-network] WARN: DNS ${DOMAIN} → ${DNS_IP} (expected ${STATIC_IP})" >&2
elif [[ -z "$DNS_IP" ]]; then
  echo "[verify-edge-lightsail-network] WARN: no DNS A record for ${DOMAIN} yet" >&2
else
  echo "[verify-edge-lightsail-network] ok: DNS points at static IP"
fi

if [[ -n "$DNS_IP" ]]; then
  if curl -sk --max-time 20 --noproxy '*' "https://${DOMAIN}/health" | grep -q '"status"'; then
    echo "[verify-edge-lightsail-network] ok: https://${DOMAIN}/health"
    exit 0
  fi
  echo "[verify-edge-lightsail-network] WARN: HTTPS health failed — if DNS was late, rerun with --renew-cert" >&2
  exit 2
fi

echo "[verify-edge-lightsail-network] ok: firewall; DNS/HTTPS not verified (no A record)"
exit 0
