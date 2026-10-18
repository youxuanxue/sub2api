#!/usr/bin/env bash
# Encode Content-Type whitelist SSOT helpers.
#
# Owner of the policy text: deploy/Caddyfile.
# This file never lists Content-Types — it only extracts/compares the owner's
# encode block so stage0 / local-bootstrap cannot drift by hand edit.
#
# shellcheck shell=bash

caddy_encode_owner() {
  local root="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
  printf '%s\n' "$root/deploy/Caddyfile"
}

# Raw encode { ... } block from a Caddyfile (preserves indentation/comments).
caddy_extract_encode_block() {
  local src="${1:?caddyfile}"
  awk '
    /^[[:space:]]*encode[[:space:]]*\{/ { in_block = 1 }
    in_block {
      print
      s = $0
      while (match(s, /[{}]/)) {
        if (substr(s, RSTART, 1) == "{") depth++
        else depth--
        s = substr(s, RSTART + 1)
      }
      if (depth == 0) exit
    }
  ' "$src"
}

# Normalized encode block (comments stripped, tokens space-joined) for compare.
caddy_normalize_encode_block() {
  local src="${1:?caddyfile}"
  # Prefer extracting first so other Content-Type headers outside encode
  # cannot pollute the comparison.
  caddy_extract_encode_block "$src" | sed 's/[[:space:]]*#.*$//' | awk '
    NF > 0 {
      for (field = 1; field <= NF; field++) {
        token = $field
        sub(/^["`]/, "", token)
        sub(/["`]$/, "", token)
        printf "%s%s", (field == 1 ? "" : " "), token
      }
      print ""
    }
  '
}
