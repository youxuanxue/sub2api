#!/usr/bin/env bash
# Read-only production-configured snapshot for the canonical Kiro TLS profile.
# This observes database configuration only; it does not capture ClientHello bytes.
set -euo pipefail

PROFILE_NAME="tk_canonical_kiro_cli"
SQL="
SELECT row_to_json(t) FROM (
  SELECT
    name,
    enable_grease,
    shuffle_extensions,
    cipher_suites,
    curves,
    point_formats,
    signature_algorithms,
    alpn_protocols,
    supported_versions,
    key_share_groups,
    psk_modes,
    extensions
  FROM tls_fingerprint_profiles
  WHERE name = '${PROFILE_NAME}'
  LIMIT 1
) t;
"

OUTPUT="$(docker exec tokenkey-postgres \
  psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c "$SQL")"
if [ -z "${OUTPUT//[[:space:]]/}" ]; then
  printf '{"status":"missing","profile_name":"%s"}\n' "$PROFILE_NAME"
else
  printf '%s\n' "$OUTPUT"
fi
