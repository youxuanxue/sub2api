#!/usr/bin/env bash
# Shared immutable QA runtime resolver; never follows a gateway color.
tk_resolve_qa_runtime() {
  local contract network
  contract="$(qa_docker inspect --format '{{index .Config.Labels "dev.tokenkey.qa-runtime"}}' tokenkey-qa-runtime)" || return 1
  [ "${contract}" = independent-v1 ] || return 1
  [ "$(qa_docker inspect --format '{{.State.Running}}' tokenkey-qa-runtime)" = false ] || return 1
  network="$(qa_docker inspect --format '{{.HostConfig.NetworkMode}}' tokenkey-qa-runtime)" || return 1
  case "${network}" in ""|default|bridge|host|none|container:*) return 1 ;; esac
  qa_docker network inspect "${network}" >/dev/null || return 1
  printf '%s\n' tokenkey-qa-runtime
}

tk_qa_runtime_network() {
  qa_docker inspect --format '{{.HostConfig.NetworkMode}}' "$1"
}
