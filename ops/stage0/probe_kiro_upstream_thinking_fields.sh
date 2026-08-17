#!/usr/bin/env bash
# Direct upstream Kiro thinking/signature field probe for one edge OAuth account.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:-}"
MODEL="${MODEL:-auto}"
MESSAGE="${MESSAGE:-What is 17+25? Reply with only the number.}"
PROBE_PY="${PROBE_PY:-/tmp/probe_upstream_thinking_fields.py}"

fail_json() {
  python3 - "$1" <<'PY'
import json, sys
print(json.dumps({"verdict": "setup_error", "error": sys.argv[1]}, ensure_ascii=False))
PY
  exit 0
}

if [[ ! "$ACCOUNT_ID" =~ ^[0-9]+$ ]]; then
  fail_json "ACCOUNT_ID must be numeric"
fi
if [[ ! -f "$PROBE_PY" ]]; then
  fail_json "missing probe_upstream_thinking_fields.py companion"
fi

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)
PROBE_DIR="$(mktemp -d /tmp/tk-kiro-thinking-probe.XXXXXX)"
CREDENTIALS_FILE="$PROBE_DIR/credentials.json"
cleanup() {
  rm -f "$CREDENTIALS_FILE"
  rmdir "$PROBE_DIR" 2>/dev/null || true
}
trap cleanup EXIT

"${PSQL[@]}" -c "
SELECT credentials::text
FROM accounts
WHERE id = ${ACCOUNT_ID}
  AND deleted_at IS NULL
  AND platform = 'kiro'
  AND type = 'oauth';
" >"$CREDENTIALS_FILE"
chmod 600 "$CREDENTIALS_FILE"

python3 "$PROBE_PY" "$CREDENTIALS_FILE" --model "$MODEL" --message "$MESSAGE"
