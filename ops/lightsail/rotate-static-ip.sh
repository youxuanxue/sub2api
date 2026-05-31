#!/usr/bin/env bash
# Lightsail Edge IP rotation primitive.
#
# Mirrors the intent of the EC2 EIP-rotation skill (tokenkey-stage0-edge-ip-
# rotation) — swap to a fresh public IP when the live one is risk-blocked /
# "polluted" — but uses Lightsail Static IP APIs instead of EC2 EIP. Three-step
# swap with explicit verification gates:
#
#   1) allocate a new Static IP under <static_ip_name>-rotated-<ts>
#   2) detach the old Static IP from the Lightsail instance, attach the new one
#   3) release the old Static IP only after the new attach is confirmed
#   4) append the released IP to deploy/aws/stage0/edge-polluted-ips.json
#
# DNS is intentionally NOT touched here — the operator updates Porkbun once
# the new IP is verified clean. Same posture as the EC2 EIP rotation skill:
# rotation primitive owns AWS state; DNS swap is the human gate.
#
# Usage:
#   bash ops/lightsail/rotate-static-ip.sh <edge_id> [--apply] [--reason '...']
# Without --apply the script only prints the plan (no AWS mutation).

set -euo pipefail

EDGE_ID=""
APPLY=""
REASON=""
MAX_ATTEMPTS="${LIGHTSAIL_ALLOCATE_MAX_ATTEMPTS:-5}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply)
      APPLY=--apply
      shift
      ;;
    --reason)
      REASON="${2:-}"
      shift 2
      ;;
    --max-attempts)
      MAX_ATTEMPTS="${2:-5}"
      shift 2
      ;;
    -h|--help)
      echo "usage: $0 <edge_id> [--apply] [--reason 'upstream API risk-block ...'] [--max-attempts N]" >&2
      exit 0
      ;;
    --*)
      echo "unknown option: $1" >&2
      exit 1
      ;;
    *)
      if [[ -n "$EDGE_ID" ]]; then
        echo "unexpected argument: $1" >&2
        exit 1
      fi
      EDGE_ID="$1"
      shift
      ;;
  esac
done

if [[ -z "$EDGE_ID" ]]; then
  echo "usage: $0 <edge_id> [--apply] [--reason '...']" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && cd .. && pwd)"
MATRIX="${REPO_ROOT}/deploy/aws/lightsail/edge-targets-lightsail.json"
REGISTRY="${REPO_ROOT}/deploy/aws/stage0/edge-polluted-ips.json"
RECORD_PY="${REPO_ROOT}/deploy/aws/stage0/record-polluted-ip.py"

if [[ ! -f "$MATRIX" ]]; then
  echo "lightsail matrix not found: $MATRIX" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required for ops/lightsail/rotate-static-ip.sh" >&2
  exit 1
fi

region="$(jq -r --arg id "$EDGE_ID" '.targets[$id].lightsail_region // empty' "$MATRIX")"
instance_name="$(jq -r --arg id "$EDGE_ID" '.targets[$id].instance_name // empty' "$MATRIX")"
static_ip_name="$(jq -r --arg id "$EDGE_ID" '.targets[$id].static_ip_name // empty' "$MATRIX")"
domain="$(jq -r --arg id "$EDGE_ID" '.targets[$id].domain // empty' "$MATRIX")"
ssm_prefix="$(jq -r --arg id "$EDGE_ID" '.targets[$id].ssm_prefix // empty' "$MATRIX")"

if [[ -z "$region" || -z "$instance_name" || -z "$static_ip_name" || -z "$domain" ]]; then
  echo "edge_id ${EDGE_ID} not fully defined in matrix" >&2
  exit 1
fi

is_excluded_ip() {
  local ip="$1" rc
  python3 "$RECORD_PY" is-excluded --ip "$ip" --region "$region" --registry "$REGISTRY"
  rc=$?
  case "$rc" in
    0) return 0 ;;
    1) return 1 ;;
    *)
      echo "::error::exclusion registry check failed for ${ip} (exit ${rc})" >&2
      exit 1
      ;;
  esac
}

# Read the live IP before any mutation so the verification gate is honest.
old_ip="$(aws lightsail get-static-ip --region "$region" --static-ip-name "$static_ip_name" \
  --query 'staticIp.ipAddress' --output text 2>/dev/null || true)"
if [[ -z "$old_ip" || "$old_ip" == "None" ]]; then
  echo "::error::no live Static IP attached for ${EDGE_ID} (run provision first)" >&2
  exit 1
fi

ts="$(date -u +%Y%m%dT%H%M%SZ)"
new_name="${static_ip_name}-rot-${ts}"

cat <<PLAN
=== Lightsail Edge IP rotation plan ===
edge_id           : ${EDGE_ID}
lightsail_region  : ${region}
instance_name     : ${instance_name}
domain            : ${domain}
old_static_ip_name: ${static_ip_name}
old_ip            : ${old_ip}
new_static_ip_name: ${new_name}
ssm_prefix        : ${ssm_prefix}
max_attempts      : ${MAX_ATTEMPTS}

Steps (each is irreversible without re-allocation):
  1) allocate Static IP ${new_name} (retry if allocation lands on excluded IP)
  2) detach ${static_ip_name} (=> ${old_ip}) from ${instance_name}
  3) attach ${new_name} to ${instance_name}; read NEW ip
  4) write ${ssm_prefix}/public_ip = NEW ip
  5) release ${static_ip_name}
  6) append ${old_ip} to ${REGISTRY}
  7) human gate: update Porkbun A record ${domain} -> NEW ip

PLAN

if [[ "$APPLY" != "--apply" ]]; then
  echo "(dry run — pass --apply to execute)"
  exit 0
fi

attempt=1
new_ip=""
while [[ "$attempt" -le "$MAX_ATTEMPTS" ]]; do
  candidate_name="${new_name}"
  if [[ "$attempt" -gt 1 ]]; then
    candidate_name="${static_ip_name}-rot-${ts}-${attempt}"
  fi

  echo "[1/6] allocate ${candidate_name} (attempt ${attempt}/${MAX_ATTEMPTS})"
  aws lightsail allocate-static-ip --region "$region" --static-ip-name "$candidate_name" >/dev/null

  candidate_ip="$(aws lightsail get-static-ip --region "$region" --static-ip-name "$candidate_name" \
    --query 'staticIp.ipAddress' --output text)"
  if [[ -z "$candidate_ip" || "$candidate_ip" == "None" ]]; then
    echo "::error::failed to read candidate Static IP after allocate" >&2
    exit 1
  fi

  if is_excluded_ip "$candidate_ip"; then
    echo "  candidate ${candidate_ip} is in exclusion registry; releasing and retrying"
    aws lightsail release-static-ip --region "$region" --static-ip-name "$candidate_name" >/dev/null
    attempt=$((attempt + 1))
    continue
  fi

  new_name="$candidate_name"
  new_ip="$candidate_ip"
  break
done

if [[ -z "$new_ip" ]]; then
  echo "::error::exhausted ${MAX_ATTEMPTS} attempts; every allocation landed on an excluded IP" >&2
  exit 1
fi

echo "[2/6] detach ${static_ip_name}"
aws lightsail detach-static-ip --region "$region" --static-ip-name "$static_ip_name" >/dev/null

echo "[3/6] attach ${new_name} to ${instance_name}"
aws lightsail attach-static-ip --region "$region" --static-ip-name "$new_name" \
  --instance-name "$instance_name" >/dev/null

attached_ip="$(aws lightsail get-static-ip --region "$region" --static-ip-name "$new_name" \
  --query 'staticIp.ipAddress' --output text)"
if [[ -z "$attached_ip" || "$attached_ip" == "None" ]]; then
  echo "::error::failed to read NEW Static IP after attach; aborting before release" >&2
  exit 1
fi
new_ip="$attached_ip"
echo "  new_ip = ${new_ip}"

if [[ -n "$ssm_prefix" ]]; then
  echo "[4/6] write ${ssm_prefix}/public_ip = ${new_ip}"
  aws ssm put-parameter --region "$region" --name "${ssm_prefix}/public_ip" \
    --type String --value "$new_ip" --overwrite >/dev/null
fi

echo "[5/6] release ${static_ip_name}"
aws lightsail release-static-ip --region "$region" --static-ip-name "$static_ip_name" >/dev/null

today="$(date -u +%F)"
rotation_reason="${REASON:-upstream API risk-block (${today})}"
registry_notes="${rotation_reason}; static_ip_name=${static_ip_name}; released via ops/lightsail/rotate-static-ip.sh"

echo "[6/6] record ${old_ip} in exclusion registry"
python3 "$RECORD_PY" append \
  --ip "$old_ip" \
  --region "$region" \
  --edge-id "$EDGE_ID" \
  --platform lightsail \
  --notes "$registry_notes"

cat <<DONE

=== rotation applied ===
edge_id : ${EDGE_ID}
old_ip  : ${old_ip}
new_ip  : ${new_ip}
domain  : ${domain}
registry: ${REGISTRY} (commit this file + run scripts/edge-ip-status.sh --check)

NEXT (human):
  - commit deploy/aws/stage0/edge-polluted-ips.json and sync docs/deploy/tokenkey-edge-ip-history.md
  - Porkbun A record ${domain} -> ${new_ip}
  - external probe from a clean-egress host:
      curl -sS --resolve ${domain}:443:${new_ip} https://${domain}/health
  - after DNS propagation:
      gh workflow run deploy-edge-lightsail-stage0.yml \\
        -f edge_id=${EDGE_ID} -f operation=smoke \\
        -f confirm_instance=${instance_name}
DONE
