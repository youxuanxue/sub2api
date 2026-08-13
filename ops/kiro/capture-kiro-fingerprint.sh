#!/usr/bin/env bash
# Capture complete TLS, HTTP, protocol, auth, and response evidence from the
# actual Kiro CLI. No IDE trigger, passive pcap, sudo, or caller-supplied cohort.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY="$SCRIPT_DIR/capture_kiro_fingerprint.py"
MITM_ADDON="$SCRIPT_DIR/mitm_kiro_http_logger.py"
KIRO_CLI="${TOKENKEY_KIRO_CLI:-/opt/homebrew/bin/kiro-cli}"
MITMDUMP="${TOKENKEY_MITMDUMP:-/opt/homebrew/bin/mitmdump}"
MITM_PORT="${TOKENKEY_KIRO_CAPTURE_PORT:-18091}"
MITM_CA="${TOKENKEY_KIRO_CAPTURE_CA:-$HOME/.mitmproxy/mitmproxy-ca-cert.pem}"
AUTH_CACHE="${TOKENKEY_KIRO_AUTH_CACHE:-$HOME/.aws/sso/cache/kiro-auth-token.json}"
OUT_DIR="${TOKENKEY_KIRO_CAPTURE_OUT_DIR:-$REPO_ROOT/.kiro_tls}"
SAMPLES="${TOKENKEY_KIRO_CAPTURE_SAMPLES:-3}"

usage() {
  cat <<'EOF'
Usage:
  capture-kiro-fingerprint.sh capture [--samples N] [--port N] [--out-dir DIR]
  capture-kiro-fingerprint.sh check --bundle PATH
  capture-kiro-fingerprint.sh diff --bundle PATH
  capture-kiro-fingerprint.sh check-replay --tls-jsonl PATH
  capture-kiro-fingerprint.sh show-baseline
  capture-kiro-fingerprint.sh emit-profile --bundle PATH [--out PATH]
  capture-kiro-fingerprint.sh version

capture launches mitmdump, runs the real `kiro-cli translate` operation at least
three times, and emits one ignored evidence bundle. The collector source-redacts
credentials, profile ARNs, user content, raw bodies, response bodies, and TLS
key-share bytes.

Exit codes: 0 complete/aligned, 1 baseline drift, 2 invalid/execution failure,
3 incomplete/NOT_OBSERVED.
EOF
}

require_executable() {
  [[ -x "$1" ]] || { printf 'error: executable not found: %s\n' "$1" >&2; exit 2; }
}

kiro_version() {
  local output
  require_executable "$KIRO_CLI"
  output="$($KIRO_CLI --version 2>/dev/null)"
  if [[ ! "$output" =~ ^kiro-cli[[:space:]]+([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
    printf 'error: expected "kiro-cli X.Y.Z" from %s\n' "$KIRO_CLI" >&2
    exit 2
  fi
  printf '%s\n' "${BASH_REMATCH[1]}"
}

wait_for_proxy() {
  python3 - "$MITM_PORT" <<'PY'
import socket, sys, time
port = int(sys.argv[1])
for _ in range(100):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.1):
            raise SystemExit(0)
    except OSError:
        time.sleep(0.1)
raise SystemExit(2)
PY
}

cmd_capture() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --samples) SAMPLES="$2"; shift 2 ;;
      --port) MITM_PORT="$2"; shift 2 ;;
      --out-dir) OUT_DIR="$2"; shift 2 ;;
      *) printf 'error: unknown argument: %s\n' "$1" >&2; exit 2 ;;
    esac
  done
  [[ "$SAMPLES" =~ ^[0-9]+$ ]] && (( SAMPLES >= 3 )) || {
    echo "error: --samples must be an integer >= 3" >&2
    exit 2
  }
  require_executable "$MITMDUMP"
  command -v python3 >/dev/null 2>&1 || { echo "error: python3 not found" >&2; exit 2; }
  [[ -r "$MITM_CA" ]] || {
    printf 'error: mitm CA not found: %s (run mitmdump once to initialize it)\n' "$MITM_CA" >&2
    exit 2
  }
  [[ -r "$AUTH_CACHE" ]] || { printf 'error: Kiro CLI auth cache not found: %s\n' "$AUTH_CACHE" >&2; exit 2; }

  local version stamp tls_log http_log whoami_file bundle mitm_log mitm_pid rc=0
  version="$(kiro_version)"
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$OUT_DIR"
  tls_log="$OUT_DIR/${stamp}-kiro-cli-tls.jsonl"
  http_log="$OUT_DIR/${stamp}-kiro-cli-http.jsonl"
  whoami_file="$OUT_DIR/${stamp}-kiro-cli-whoami.txt"
  bundle="$OUT_DIR/${stamp}-kiro-cli.bundle.json"
  mitm_log="$OUT_DIR/${stamp}-mitmdump.log"

  KIRO_CAPTURE_TLS_LOG="$tls_log" KIRO_CAPTURE_HTTP_LOG="$http_log" \
    "$MITMDUMP" --listen-host 127.0.0.1 --listen-port "$MITM_PORT" \
    --set connection_strategy=lazy -s "$MITM_ADDON" >"$mitm_log" 2>&1 &
  mitm_pid=$!
  cleanup() {
    if kill -0 "$mitm_pid" 2>/dev/null; then
      kill "$mitm_pid" 2>/dev/null || true # preflight-allow: cleanup
      wait "$mitm_pid" 2>/dev/null || true # preflight-allow: cleanup
    fi
  }
  trap cleanup EXIT INT TERM
  wait_for_proxy || { echo "error: mitmdump did not become ready" >&2; exit 2; }

  "$KIRO_CLI" whoami --format json 2>/dev/null | python3 -c '
import json, sys
value = json.load(sys.stdin)
account_type = value.get("accountType")
region = value.get("region")
if account_type not in {"BuilderId", "Enterprise"} or not isinstance(region, str) or not region:
    raise SystemExit("error: kiro-cli whoami returned unrecognized metadata")
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump({"account_type": account_type, "region": region}, handle, sort_keys=True)
    handle.write("\n")
' "$whoami_file"

  local proxy="http://127.0.0.1:$MITM_PORT" attempt
  for ((attempt = 1; attempt <= SAMPLES; attempt++)); do
    if ! printf '%s\n' 'list files' | env \
      HTTP_PROXY="$proxy" HTTPS_PROXY="$proxy" http_proxy="$proxy" https_proxy="$proxy" \
      SSL_CERT_FILE="$MITM_CA" "$KIRO_CLI" translate >/dev/null; then
      printf 'error: real kiro-cli request %d failed\n' "$attempt" >&2
      exit 2
    fi
  done
  cleanup
  trap - EXIT INT TERM

  python3 "$PY" bundle \
    --tls-jsonl "$tls_log" \
    --http-jsonl "$http_log" \
    --auth-cache "$AUTH_CACHE" \
    --whoami-file "$whoami_file" \
    --kiro-cli-version "$version" \
    --minimum-tls-samples "$SAMPLES" \
    --out "$bundle" || rc=$?
  printf 'bundle=%s\n' "$bundle"
  return "$rc"
}

main() {
  local command="${1:-}"
  [[ $# -eq 0 ]] || shift
  case "$command" in
    capture) cmd_capture "$@" ;;
    check|diff) exec python3 "$PY" check "$@" ;;
    check-replay) exec python3 "$PY" check-replay "$@" ;;
    show-baseline) exec python3 "$PY" show-baseline ;;
    emit-profile) exec python3 "$PY" emit-profile "$@" ;;
    version) kiro_version ;;
    -h|--help|"") usage ;;
    *) printf 'error: unknown command: %s\n' "$command" >&2; usage >&2; exit 2 ;;
  esac
}

main "$@"
