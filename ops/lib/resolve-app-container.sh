#!/usr/bin/env bash
# Canonical TokenKey app-container resolver. SOURCE this file; do not exec it.
#
# Every host-side probe, diagnostic, and guarded maintenance path resolves the
# live application container through this one function. Before it existed the
# same active-color + candidate-loop logic was copy-pasted across ~16 sites in
# three different shapes (bash functions, python heredocs, inline SSM command
# strings) and the copies had already diverged: most only asked whether a
# container *existed*, so during a blue/green window they happily selected a
# STOPPED container and reported its stale env as live runtime state.
#
# Contract (fail closed, never guess):
#   1. An explicit container name must itself be running, or resolution fails.
#   2. active-color is authoritative when it names blue|green AND that container
#      is running.
#   3. Otherwise enumerate tokenkey, tokenkey-blue, tokenkey-green and accept the
#      result only when EXACTLY ONE is running. Zero or several running
#      candidates is ambiguous, and ambiguity resolves to failure rather than to
#      a positional guess.
#
# Running-ness — not mere existence — is the test, because a diagnostic that
# reads a stopped container is worse than one that reports "unknown": it looks
# like a healthy answer.
#
# Callers configure via environment:
#   ACTIVE_COLOR_FILE  default /var/lib/tokenkey/active-color (test seam)
#   TK_DOCKER          docker CLI prefix, e.g. "sudo docker" (default "docker")
#
# Usage:
#   . "$(dirname "$0")/../lib/resolve-app-container.sh"
#   container="$(tk_resolve_app_container "${APP_CONTAINER:-auto}")" || {
#     echo "app container unresolved" >&2; exit 1; }

ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
TK_DOCKER="${TK_DOCKER:-docker}"

TK_APP_CONTAINER_CANDIDATES="tokenkey tokenkey-blue tokenkey-green"

# tk_container_running NAME -> 0 when the container exists and State.Running is true.
tk_container_running() {
  [ -n "${1:-}" ] || return 1
  local running
  # shellcheck disable=SC2086  # TK_DOCKER may legitimately be "sudo docker".
  running="$($TK_DOCKER inspect --format '{{.State.Running}}' "$1" 2>/dev/null)" || return 1
  [ "$running" = true ]
}

# tk_resolve_app_container [NAME|auto] -> prints the resolved container on stdout.
# Returns non-zero (printing nothing) when the container cannot be resolved
# unambiguously. Callers MUST treat that as unknown and fail closed.
tk_resolve_app_container() {
  local requested="${1:-auto}"
  local color candidate found="" count=0

  if [ -n "$requested" ] && [ "$requested" != auto ]; then
    tk_container_running "$requested" || return 1
    printf '%s\n' "$requested"
    return 0
  fi

  if [ -r "$ACTIVE_COLOR_FILE" ]; then
    # An unreadable, empty, or garbage active-color is not fatal on its own: the
    # unambiguous-single-running fallback below can still answer safely.
    color="$(tr -d '[:space:]' < "$ACTIVE_COLOR_FILE" 2>/dev/null)" || color=""
    case "$color" in
      blue | green)
        candidate="tokenkey-$color"
        if tk_container_running "$candidate"; then
          printf '%s\n' "$candidate"
          return 0
        fi
        ;;
    esac
  fi

  for candidate in $TK_APP_CONTAINER_CANDIDATES; do
    if tk_container_running "$candidate"; then
      found="$candidate"
      count=$((count + 1))
    fi
  done
  [ "$count" -eq 1 ] || return 1
  printf '%s\n' "$found"
}
