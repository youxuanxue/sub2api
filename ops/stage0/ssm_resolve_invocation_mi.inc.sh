#!/usr/bin/env bash
# shellcheck shell=bash
#
# Canonical SSM "resolve managed-instance" helper, sourced by every Stage0 ops
# script that tag-targets a Lightsail Hybrid edge with `ssm send-command`.
#
# Why a shared include: after a tag-targeted SendCommand the concrete mi-* id is
# not known up front, but get-command-invocation requires it explicitly. Four
# scripts (deploy_via_ssm.sh, sync_caddyfile_via_ssm.sh, edge_post_deploy_smoke.sh,
# and the capture/reset edge-admin helpers) each used to inline a byte-identical
# 180s poll loop. This file is the single source of truth for that loop.
#
# verify-edge-lightsail-network.sh intentionally does NOT use this helper: it
# resolves the mi via a one-shot `list-command-invocations --details` + `ssm wait
# command-executed`, a different control flow that does not poll.
#
# Usage:
#   source "$(dirname "${BASH_SOURCE[0]}")/ssm_resolve_invocation_mi.inc.sh"
#   mi="$(ssm_resolve_invocation_mi "<region>" "<command-id>")"
#
# Args:
#   $1 region     AWS region; pass "" to omit --region (AWS default chain).
#   $2 command-id SSM command id returned by a tag-targeted send-command.
# Output: the single managed-instance id on stdout.
# Returns: 0 on success; 1 on timeout / not-exactly-one invocation. Callers run
#   under `set -e` with `mi="$(ssm_resolve_invocation_mi ...)"`, so a non-zero
#   return aborts the script the same way the old inline `exit 1` did.

ssm_resolve_invocation_mi() {
  local region="$1"
  local cmd_id="$2"
  local region_args=()
  [[ -n "${region}" ]] && region_args=(--region "${region}")
  local cutoff=$(( $(date +%s) + 180 ))
  while [[ $(date +%s) -lt "${cutoff}" ]]; do
    local json n
    json="$(aws "${region_args[@]}" ssm list-command-invocations \
      --command-id "${cmd_id}" --output json 2>/dev/null || echo '{"CommandInvocations":[]}')"
    n="$(echo "${json}" | jq '.CommandInvocations | length')"
    if [[ "${n}" -ge 1 ]]; then
      if [[ "${n}" -ne 1 ]]; then
        echo "ssm_resolve_invocation_mi: expected exactly one SSM invocation for command=${cmd_id}, got ${n}" >&2
        echo "${json}" | jq '.' >&2
        return 1
      fi
      echo "${json}" | jq -r '.CommandInvocations[0].InstanceId'
      return 0
    fi
    sleep 3
  done
  echo "ssm_resolve_invocation_mi: timed out resolving invocation for command=${cmd_id}" >&2
  return 1
}
