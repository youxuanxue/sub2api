#!/usr/bin/env bash
# Direct, read-only Kiro account/model probe for an edge host.
# Credentials stay in a mode-600 temp file and are never written to stdout.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:-}"
MODELS="${MODELS:-claude-opus-5}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-60}"
KIRO_PROBE_PY="${KIRO_PROBE_PY:-/tmp/probe_runtime_gateway.py}"
KIRO_CONSTANTS_GO="${KIRO_CONSTANTS_GO:-/tmp/constants.go}"

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
if [[ -z "$MODELS" ]]; then
  fail_json "MODELS must contain at least one model id"
fi
if [[ ! "$REQUEST_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$REQUEST_TIMEOUT_SECONDS" -lt 1 ]]; then
  fail_json "REQUEST_TIMEOUT_SECONDS must be a positive integer"
fi
if [[ ! -f "$KIRO_PROBE_PY" ]]; then
  fail_json "missing probe_runtime_gateway.py companion"
fi
if [[ ! -f "$KIRO_CONSTANTS_GO" ]]; then
  fail_json "missing Kiro constants.go companion"
fi
export KIRO_CONSTANTS_GO

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)
META="$("${PSQL[@]}" -c "
SELECT COALESCE(row_to_json(t)::text, '')
FROM (
  SELECT id, platform, type, status, schedulable,
         COALESCE(credentials->>'expires_at', '') AS expires_at
  FROM accounts
  WHERE id = ${ACCOUNT_ID} AND deleted_at IS NULL
) t;
" | tr -d '\n')"
if [[ -z "$META" ]]; then
  fail_json "target account not found"
fi

VALIDATION="$(python3 - "$META" <<'PY'
import json, sys
row = json.loads(sys.argv[1])
if row.get("platform") != "kiro":
    print("target account platform is not kiro")
elif row.get("type") != "oauth":
    print("target account type is not oauth")
else:
    print("")
PY
)"
if [[ -n "$VALIDATION" ]]; then
  fail_json "$VALIDATION"
fi

PROBE_DIR="$(mktemp -d /tmp/tk-kiro-model-probe.XXXXXX)"
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

if ! python3 - "$CREDENTIALS_FILE" <<'PY'
import json, sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
if not str(payload.get("access_token") or "").strip():
    raise SystemExit(1)
PY
then
  fail_json "target account has no access_token"
fi

python3 - "$META" "$MODELS" "$REQUEST_TIMEOUT_SECONDS" "$CREDENTIALS_FILE" "$KIRO_PROBE_PY" <<'PY'
import importlib.util
import argparse
import json
import os
import sys
from pathlib import Path
from urllib.parse import quote

meta = json.loads(sys.argv[1])
models = sys.argv[2].split()
timeout = int(sys.argv[3])
credentials_file = Path(sys.argv[4])
probe_path = Path(sys.argv[5])
sys.path.insert(0, str(probe_path.parent))

spec = importlib.util.spec_from_file_location("kiro_runtime_probe", probe_path)
if spec is None or spec.loader is None:
    raise SystemExit("unable to load Kiro runtime probe")
probe = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = probe
spec.loader.exec_module(probe)

token = probe.load_local_token(credentials_file)
profile_path = Path(os.environ["TOKENKEY_KIRO_CLI_PROFILE"])
identity = probe.load_cli_identity(profile_path)
args = argparse.Namespace(
    token_cache=str(credentials_file),
    profile_arn=str(token.get("profile_arn") or ""),
    proxy="",
    timeout=timeout,
    model_id="",
)
try:
    token, _ = probe.prepare_token(args, identity, needs_profile=True, needs_model=False)
except probe.ProbeEnvError as exc:
    print(json.dumps({
        "verdict": "setup_error",
        "account_id": meta["id"],
        "error": str(exc),
    }, ensure_ascii=False))
    raise SystemExit(0)

try:
    profile_arn = str(token.get("profile_arn") or "")
    query = "origin=AI_EDITOR&maxResults=100"
    if profile_arn:
        query += "&profileArn=" + quote(profile_arn, safe="")
    catalog = probe.http_json(
        method="GET",
        url=f"{probe.MANAGEMENT_ENDPOINT}/List-Available-Models?{query}",
        token=token,
        identity=identity,
        proxy=None,
        timeout=timeout,
    )
    advertised_models = probe.parse_model_ids(catalog)
    print(json.dumps({
        "verdict": "catalog_snapshot",
        "account_id": meta["id"],
        "sonnet_models": [model for model in advertised_models if "sonnet" in model.lower()],
    }, ensure_ascii=False))
except probe.ProbeEnvError as exc:
    print(json.dumps({
        "verdict": "catalog_unavailable",
        "account_id": meta["id"],
        "error": str(exc),
    }, ensure_ascii=False))

for model in models:
    request = probe.build_runtime_chat_spec(
        token=token,
        identity=identity,
        message="Reply OK only.",
        model_id=model,
    )
    result = probe.execute_probe(request, timeout=timeout, proxy=None)
    if result.ok:
        verdict = "servable"
    elif result.status is None:
        verdict = "inconclusive_transport"
    else:
        verdict = "upstream_rejected"
    print(json.dumps({
        "verdict": verdict,
        "account_id": meta["id"],
        "model": model,
        "http_status": result.status,
        "account_status": meta.get("status"),
        "account_schedulable": meta.get("schedulable"),
        "expires_at": meta.get("expires_at"),
        "error": result.error,
    }, ensure_ascii=False))
PY
