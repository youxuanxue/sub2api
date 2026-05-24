#!/usr/bin/env bash
# shellcheck shell=bash
# Sourced by capture/reset edge admin helpers — resolves Hybrid mi-* after tag-target SendCommand.

edge_admin_resolve_ssm_invocation_mi() {
  local region="$1"
  local cmd_id="$2"
  local cutoff=$(( $(date +%s) + 180 ))
  while [[ $(date +%s) -lt "${cutoff}" ]]; do
    local json n mi
    json="$(aws ssm list-command-invocations \
      --region "${region}" \
      --command-id "${cmd_id}" \
      --output json 2>/dev/null || echo '{"CommandInvocations":[]}')"
    n="$(echo "${json}" | jq '.CommandInvocations | length')"
    if [[ "${n}" -ge 1 ]]; then
      if [[ "${n}" -ne 1 ]]; then
        echo "[error] edge_admin_ssm_resolve: expected one invocation for command=${cmd_id}, got ${n}" >&2
        echo "${json}" | jq '.' >&2
        return 1
      fi
      mi="$(echo "${json}" | jq -r '.CommandInvocations[0].InstanceId')"
      echo "${mi}"
      return 0
    fi
    sleep 3
  done
  echo "[error] edge_admin_ssm_resolve: timed out resolving invocation for command=${cmd_id}" >&2
  return 1
}
