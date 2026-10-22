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

# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
OUTPUT="$(docker exec "$TK_PG" \
  psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c "$SQL")"
if [ -z "${OUTPUT//[[:space:]]/}" ]; then
  printf '{"status":"missing","profile_name":"%s"}\n' "$PROFILE_NAME"
else
  printf '%s\n' "$OUTPUT"
fi
