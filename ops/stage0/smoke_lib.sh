#!/usr/bin/env bash
# Shared helpers for Stage0 gateway smoke scripts.
# Source from post_deploy_smoke.sh / edge_post_deploy_smoke.sh — do not execute directly.
set -euo pipefail

: "${GATEWAY_SMOKE_SUITE:=full}"

# Canonical suite names (one runner, multiple deploy intents):
#   full            — prod deploy-stage0: all gateway probes
#   main-via-edge   — prod→edge canary: public + models + /v1/messages (claude-cli UA)
#   quick           — manual gateway_smoke.sh equivalent: public + models + chat only
if [[ -z "${_TK_SMOKE_LIB_LOADED:-}" ]]; then
  _TK_SMOKE_LIB_LOADED=1

  smoke_normalize_suite() {
    case "${1:-full}" in
      full|prod) echo "full" ;;
      main-via-edge|edge-via-prod) echo "main-via-edge" ;;
      quick|minimal) echo "quick" ;;
      *)
        echo "tk_smoke: unknown GATEWAY_SMOKE_SUITE='${1}' (want full|main-via-edge|quick)" >&2
        return 1
        ;;
    esac
  }

  GATEWAY_SMOKE_SUITE="$(smoke_normalize_suite "${GATEWAY_SMOKE_SUITE}")"

  smoke_suite_runs() {
    local section="$1"
    case "${GATEWAY_SMOKE_SUITE}" in
      full)
        return 0
        ;;
      main-via-edge)
        case "${section}" in
          public|models|messages) return 0 ;;
          *) return 1 ;;
        esac
        ;;
      quick)
        case "${section}" in
          public|models|chat) return 0 ;;
          *) return 1 ;;
        esac
        ;;
    esac
    return 1
  }

  smoke_default_claude_user_agent() {
    printf '%s' "${TK_SMOKE_CLAUDE_USER_AGENT:-claude-cli/2.1.197 (external, sdk-cli)}"
  }

  # smoke_model_list RAW DEFAULT
  # Prints a newline-delimited model list. RAW may be comma or whitespace
  # separated so GitHub Environment vars can stay compact.
  smoke_model_list() {
    local raw="${1:-}"
    local default="${2:-}"
    local item
    raw="${raw:-${default}}"
    raw="${raw//$'\r'/ }"
    raw="${raw//,/ }"
    for item in ${raw}; do
      [[ -n "${item}" ]] && printf '%s\n' "${item}"
    done
  }

  smoke_assert_model_listed() {
    local models_file="$1"
    local label="$2"
    local model="$3"
    if jq -e --arg m "${model}" '(.data // []) | any(.id == $m)' "${models_file}" >/dev/null 2>&1; then
      return 0
    fi
    echo "::error::tk_post_deploy_smoke: configured ${label} model '${model}' is not listed by /v1/models for this smoke key" >&2
    echo "available models:" >&2
    jq -r '(.data // [])[] | .id' "${models_file}" >&2 || true
    return 1
  }

  # smoke_assert_anthropic_model_listed_or_warn — universal-key prod topology:
  # TK_SMOKE_API_KEY is routing_mode=universal; GET /v1/models skips backing-group
  # resolution (docs/approved/universal-key-routing.md PR1: metadata fallback, PR3:
  # entitled-group union). The handler unions global account model_mapping keys, so
  # newapi vendor ids appear while prod Claude capacity (kiro-us3/4/5/6 mirror stubs
  # with empty native mapping) does not. /v1/messages is the canonical Anthropic probe.
  smoke_assert_anthropic_model_listed_or_warn() {
    local models_file="$1"
    local model="$2"
    if jq -e --arg m "${model}" '(.data // []) | any(.id == $m)' "${models_file}" >/dev/null 2>&1; then
      return 0
    fi
    echo "::warning::tk_post_deploy_smoke: anthropic model '${model}' missing from /v1/models for universal smoke key — expected when prod Claude is only via kiro mirror stubs (empty model_mapping); deferring to /v1/messages probe" >&2
    return 0
  }

  # smoke_assert_openai_oauth_model_listed_or_warn — same universal-key topology as
  # anthropic: OpenAI OAuth accounts may use empty model_mapping passthrough to
  # upstream model ids. GET /v1/models omits those ids; /v1/chat/completions
  # (openai oauth) is the canonical probe.
  smoke_assert_openai_oauth_model_listed_or_warn() {
    local models_file="$1"
    local model="$2"
    if jq -e --arg m "${model}" '(.data // []) | any(.id == $m)' "${models_file}" >/dev/null 2>&1; then
      return 0
    fi
    echo "::warning::tk_post_deploy_smoke: openai_oauth model '${model}' missing from /v1/models for universal smoke key — expected when prod OpenAI OAuth uses empty model_mapping passthrough; deferring to /v1/chat/completions (openai oauth) probe" >&2
    return 0
  }

  # soft_degrade_or_exit — see post_deploy_smoke.sh header for contract.
  soft_degrade_or_exit() {
    local label="$1" http="$2" resp_file="$3"
    local err_msg
    err_msg="$(jq -r '.error.message // empty' "${resp_file}" 2>/dev/null)"
    case "${http}" in
      200)
        return 0
        ;;
      5*|429)
        echo "::warning::tk_post_deploy_smoke: ${label} returned HTTP ${http} — runtime resource issue (likely no available accounts / upstream 5xx / rate-limit), NOT a control-plane regression." >&2
        if [[ -n "${err_msg}" ]]; then
          echo "  gateway message: ${err_msg}" >&2
        fi
        jq . "${resp_file}" >&2 2>/dev/null || cat "${resp_file}" >&2
        echo "tk_post_deploy_smoke: ${label} section soft-skipped (HTTP ${http} is not a shape-regression signal)"
        return 1
        ;;
      *)
        case "${err_msg}" in
          # Pool exhaustion is a runtime resource state, not a control-plane
          # regression. Two distinct gateway phrasings reach here:
          #   "no available accounts"        — empty/throttled pool on the
          #                                    directly-bound group.
          #   "All available accounts exhausted" — the CC-only fallback path
          #                                    (PR #740): a claude_code_only key
          #                                    routes /v1/chat|/v1/responses to
          #                                    its fallback_group_id, and that
          #                                    fallback pool was exhausted. Before
          #                                    #740 this same key returned the
          #                                    "restricted to Claude Code clients"
          #                                    rejection below (already soft-skipped),
          #                                    so matching only the first phrase
          #                                    turned an unchanged pool state into
          #                                    a hard smoke failure. /v1/messages
          #                                    stays the canonical signal for this key.
          *"no available accounts"*|*"available accounts exhausted"*)
            echo "::warning::tk_post_deploy_smoke: ${label} returned HTTP ${http} with a pool-exhaustion message ('no available accounts' / 'All available accounts exhausted') — pool exhausted, not a control-plane regression." >&2
            jq . "${resp_file}" >&2 2>/dev/null || cat "${resp_file}" >&2
            echo "tk_post_deploy_smoke: ${label} section soft-skipped (pool exhausted)"
            return 1
            ;;
          *"restricted to Claude Code clients"*|*"/v1/messages only"*)
            # claude_code_only group policy: the configured Anthropic key is
            # bound to a group that allows /v1/messages with a Claude Code UA
            # only. Two cases:
            #   main-via-edge suite — this branch should be unreachable
            #     (smoke_suite_runs gates out non-messages sections), so a
            #     claude_code_only hit here means the smoke runner itself is
            #     misconfigured. Hard fail to surface the config bug.
            #   full / quick suite — /v1/chat/completions against a
            #     claude_code_only-restricted key is policy-correct rejection,
            #     not a control-plane regression. The /v1/messages section
            #     (Claude Code UA) still runs and is the canonical signal for
            #     this key's group. Soft-skip with a warning.
            if [[ "${GATEWAY_SMOKE_SUITE}" == "main-via-edge" ]]; then
              echo "::error::tk_post_deploy_smoke: ${label} returned HTTP ${http} — main-via-edge must use /v1/messages with a Claude Code User-Agent, not /v1/chat/completions." >&2
              echo "tk_post_deploy_smoke: ${label} failed" >&2
              jq . "${resp_file}" >&2 2>/dev/null || cat "${resp_file}" >&2
              exit 1
            fi
            echo "::warning::tk_post_deploy_smoke: ${label} returned HTTP ${http} — configured key is bound to a Claude Code-only group (claude_code_only policy); /v1/messages section will cover this key. Soft-skipped." >&2
            if [[ -n "${err_msg}" ]]; then
              echo "  gateway message: ${err_msg}" >&2
            fi
            jq . "${resp_file}" >&2 2>/dev/null || cat "${resp_file}" >&2
            echo "tk_post_deploy_smoke: ${label} section soft-skipped (claude_code_only policy; /v1/messages probe is the canonical signal)"
            return 1
            ;;
          *)
            echo "tk_post_deploy_smoke: ${label} failed" >&2
            jq . "${resp_file}" >&2 2>/dev/null || cat "${resp_file}" >&2
            exit 1
            ;;
        esac
        ;;
    esac
  }

  # smoke_pick_model_from_list FILE [OVERRIDE]
  # Prints model id: prefer OVERRIDE when listed; else warn and keep auto pick (claude regex, else first).
  smoke_pick_model_from_list() {
    local models_file="$1"
    local override="${2:-}"
    local auto
    auto="$(jq -r '(.data // []) as $d | ($d | map(select(.id|test("claude";"i"))) | .[0].id) // $d[0].id // empty' "${models_file}")"
    if [[ -z "${auto}" || "${auto}" == "null" ]]; then
      echo "tk_post_deploy_smoke: no model id in /v1/models" >&2
      jq . "${models_file}" >&2 || true
      return 1
    fi
    if [[ -n "${override}" ]]; then
      if jq -e --arg m "${override}" '(.data // []) | any(.id == $m)' "${models_file}" >/dev/null 2>&1; then
        printf '%s' "${override}"
        return 0
      fi
      echo "::warning::tk_post_deploy_smoke: configured chat model '${override}' not listed for this key; using auto-selected model=${auto}" >&2
      jq -r '(.data // [])[] | .id' "${models_file}" >&2 || true
    fi
    printf '%s' "${auto}"
  }
fi
