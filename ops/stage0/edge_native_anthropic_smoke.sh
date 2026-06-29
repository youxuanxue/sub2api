#!/usr/bin/env bash
# edge_native_anthropic_smoke.sh — realistic per-account Anthropic OAuth smoke on an edge host.
#
# Runs ON the edge machine (SSM / run-probe). For each schedulable native OAuth account
# in the default group, binds a reserved __tk_probe_* key and sends one in-container
# /v1/messages request with Claude Code-shaped payload (smoke_anthropic_realistic.py).
#
# Env:
#   ANTHROPIC_MODELS          space/comma separated model ids (default: claude-sonnet-4-6)
#   ANTHROPIC_SOURCE_GROUP    edge OAuth pool group name (default: default)
#   PROBE_ACCOUNT_MODEL_SH    path to probe_account_model.sh (default: same dir)
#   SMOKE_ANTHROPIC_REALISTIC_PY  path to payload builder (default: same dir)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROBE_SCRIPT="${PROBE_ACCOUNT_MODEL_SH:-${SCRIPT_DIR}/probe_account_model.sh}"
REALISTIC_PY="${SMOKE_ANTHROPIC_REALISTIC_PY:-${SCRIPT_DIR}/smoke_anthropic_realistic.py}"
ANTHROPIC_SOURCE_GROUP="${ANTHROPIC_SOURCE_GROUP:-default}"
ANTHROPIC_MODELS_RAW="${ANTHROPIC_MODELS:-${TK_SMOKE_EDGE_LOCAL_CHAT_MODELS:-claude-sonnet-4-6}}"

if [[ ! -f "$PROBE_SCRIPT" ]]; then
  echo "tk_edge_native_anthropic_smoke: missing probe script: $PROBE_SCRIPT" >&2
  exit 1
fi
if [[ ! -f "$REALISTIC_PY" ]]; then
  echo "tk_edge_native_anthropic_smoke: missing realistic payload script: $REALISTIC_PY" >&2
  exit 1
fi

command -v jq >/dev/null 2>&1 || { echo "tk_edge_native_anthropic_smoke: jq required" >&2; exit 1; }

models=()
_models_raw="${ANTHROPIC_MODELS_RAW//$'\r'/ }"
_models_raw="${_models_raw//$'\n'/ }"
_models_raw="${_models_raw//$'\t'/ }"
_models_raw="${_models_raw//,/ }"
for m in ${_models_raw}; do
  [[ -n "$m" ]] && models+=("$m")
done

if [[ "${#models[@]}" -eq 0 ]]; then
  echo "tk_edge_native_anthropic_smoke: no models configured" >&2
  exit 1
fi

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

account_ids="$("${PSQL[@]}" -c "
SELECT a.id
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g ON g.id = ag.group_id
WHERE a.platform = 'anthropic'
  AND a.deleted_at IS NULL
  AND a.schedulable = true
  AND a.status = 'active'
  AND a.type IN ('oauth', 'setup_token')
  AND g.name = '${ANTHROPIC_SOURCE_GROUP//\'/''}'
  AND g.deleted_at IS NULL
ORDER BY a.id;
" | tr -d '[:space:]' | sed 's/|/ /g')"

if [[ -z "${account_ids// }" ]]; then
  echo "tk_edge_native_anthropic_smoke: no schedulable anthropic OAuth accounts in group=${ANTHROPIC_SOURCE_GROUP}" >&2
  exit 1
fi

echo "tk_edge_native_anthropic_smoke: group=${ANTHROPIC_SOURCE_GROUP} models=${models[*]} accounts=${account_ids}"

hard_fail=0
soft_only=0
served=0

for account_id in ${account_ids}; do
  for model in "${models[@]}"; do
    echo "tk_edge_native_anthropic_smoke: probe account_id=${account_id} model=${model}"
  probe_json="$(
    SMOKE_ANTHROPIC_REALISTIC_PY="$REALISTIC_PY" \
    TK_SMOKE_ANTHROPIC_REALISTIC=1 \
    PROMPT_TEXT="${PROMPT_TEXT:-hi}" \
    MAX_TOKENS="${MAX_TOKENS:-32}" \
    ACCOUNT_ID="$account_id" \
    MODEL="$model" \
    ENDPOINT=messages \
    bash "$PROBE_SCRIPT"
  )"
    verdict="$(jq -r '.verdict // empty' <<<"$probe_json")"
    http_code="$(jq -r '.http_code // empty' <<<"$probe_json")"
    echo "tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} verdict=${verdict} http=${http_code}"

    case "$verdict" in
      servable|uncorrelated_success)
        served=$((served + 1))
        ;;
      upstream_rejected)
        if [[ "$http_code" == "401" || "$http_code" == "403" ]]; then
          echo "::error::tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} auth rejected (HTTP ${http_code}) — re-OAuth or fix credentials" >&2
          jq -c '{verdict,http_code,target_account:{id,name,error_message},response:{body_excerpt}}' <<<"$probe_json" >&2 || true
          hard_fail=1
        else
          echo "::warning::tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} upstream HTTP ${http_code} (not deploy-blocking)" >&2
          soft_only=1
        fi
        ;;
      gateway_rejected)
        if [[ "$http_code" == "429" ]]; then
          echo "::warning::tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} rate-limited / pool exhausted (HTTP 429) — not a deploy regression" >&2
          soft_only=1
        else
          echo "::error::tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} gateway rejected HTTP ${http_code}" >&2
          hard_fail=1
        fi
        ;;
      wrong_account|setup_error)
        echo "::error::tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} probe ${verdict}" >&2
        jq -c '{verdict,error,probe}' <<<"$probe_json" >&2 || true
        hard_fail=1
        ;;
      *)
        echo "::error::tk_edge_native_anthropic_smoke: account_id=${account_id} model=${model} unknown verdict=${verdict}" >&2
        hard_fail=1
        ;;
    esac
  done
done

if [[ "$hard_fail" -ne 0 ]]; then
  exit 1
fi
if [[ "$served" -eq 0 && "$soft_only" -ne 0 ]]; then
  echo "tk_edge_native_anthropic_smoke: soft-skipped (runtime capacity/auth pressure; no servable 200)"
  exit 0
fi
if [[ "$served" -eq 0 ]]; then
  echo "tk_edge_native_anthropic_smoke: no successful probe" >&2
  exit 1
fi

echo "tk_edge_native_anthropic_smoke: OK served=${served}"
