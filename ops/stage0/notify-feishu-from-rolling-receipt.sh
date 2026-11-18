#!/usr/bin/env bash
# Best-effort Feishu rollout card after a successful prod/cn-prod rolling deploy.
# Reads previous_runtime_tag + finished_at from the rolling receipt so the card
# baseline is the tag that was actually serving (not git's previous release).
#
# Usage:
#   TK_FEISHU_WEBHOOK_URL=... [TK_FEISHU_SIGNING_SECRET=...] \
#     bash ops/stage0/notify-feishu-from-rolling-receipt.sh \
#       --receipt PATH --environment prod|cn-prod --api-url URL \
#       [--run-url URL]
#
# Exit 0 always (best-effort); missing baseline or webhook → ::warning:: + skip.
set -uo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash ops/stage0/notify-feishu-from-rolling-receipt.sh \
    --receipt PATH --environment prod|cn-prod --api-url URL [--run-url URL]
EOF
}

RECEIPT=""
ENVIRONMENT=""
API_URL=""
RUN_URL=""

need_val() { [ "$2" -ge 2 ] || { echo "[error] $1 needs a value" >&2; usage; exit 2; }; }

while [ $# -gt 0 ]; do
  case "$1" in
    --receipt) need_val "$1" "$#"; RECEIPT="$2"; shift 2 ;;
    --environment) need_val "$1" "$#"; ENVIRONMENT="$2"; shift 2 ;;
    --api-url) need_val "$1" "$#"; API_URL="$2"; shift 2 ;;
    --run-url) need_val "$1" "$#"; RUN_URL="$2"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    --*) echo "[error] unknown flag: $1" >&2; usage; exit 2 ;;
    *) echo "[error] unexpected arg: $1" >&2; usage; exit 2 ;;
  esac
done

if [ -z "$RECEIPT" ] || [ -z "$ENVIRONMENT" ] || [ -z "$API_URL" ]; then
  echo "[error] --receipt, --environment, and --api-url are required" >&2
  usage
  exit 2
fi
case "$ENVIRONMENT" in
  prod|cn-prod) ;;
  *) echo "[error] --environment must be prod|cn-prod" >&2; exit 2 ;;
esac
if [ ! -f "$RECEIPT" ]; then
  echo "::warning::rolling receipt missing at $RECEIPT; skip Feishu notification"
  exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Emit KEY=value lines so empty previous_runtime_tag does not collapse fields.
# shellcheck disable=SC1091
eval "$(RECEIPT="$RECEIPT" python3 - <<'PY'
import json, os, shlex, sys
path = os.environ["RECEIPT"]
try:
    data = json.load(open(path, encoding="utf-8"))
except (OSError, json.JSONDecodeError) as exc:
    print(f"echo {shlex.quote('::warning::could not parse rolling receipt; skip Feishu notification')}; exit 0")
    sys.exit(0)
def q(value: str) -> str:
    return shlex.quote(value)
tag = (data.get("tag") or "").strip().lstrip("v")
prev = (data.get("previous_runtime_tag") or "").strip().lstrip("v")
finished = (data.get("finished_at") or "").strip()
outcome = (data.get("outcome") or "").strip()
print(f"TAG={q(tag)}")
print(f"PREVIOUS_TAG={q(prev)}")
print(f"DEPLOYED_AT={q(finished)}")
print(f"OUTCOME={q(outcome)}")
PY
)"
if [ "${OUTCOME:-}" != "success" ]; then
  echo "::warning::rolling outcome is ${OUTCOME:-unknown}; skip Feishu notification"
  exit 0
fi
if [ -z "${TAG:-}" ]; then
  echo "::warning::rolling receipt has no tag; skip Feishu notification"
  exit 0
fi
if [ -z "${PREVIOUS_TAG:-}" ]; then
  echo "::warning::previous_runtime_tag empty (noop or unknown baseline); skip Feishu notification"
  exit 0
fi

NOTES="$(bash "$SCRIPT_DIR/collect-feishu-release-notes.sh" "$PREVIOUS_TAG" "$TAG" || true)"
args=(
  "$TAG" "$API_URL"
  --previous-tag "$PREVIOUS_TAG"
  --environment "$ENVIRONMENT"
  --notes "$NOTES"
)
[ -n "$RUN_URL" ] && args+=(--run-url "$RUN_URL")
[ -n "$DEPLOYED_AT" ] && args+=(--deployed-at "$DEPLOYED_AT")
bash "$SCRIPT_DIR/notify-feishu-release.sh" "${args[@]}"
