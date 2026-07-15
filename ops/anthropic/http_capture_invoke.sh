#!/usr/bin/env bash
# Invoke Claude Code for HTTP mitm capture: client -> mitm -> gost -> SOCKS -> egress.
# Uses plain claude (not cc0-here) so NODE_EXTRA_CA_CERTS / NODE_TLS_REJECT_UNAUTHORIZED apply.
# OAuth session comes from CC0_USER_OVERLAY (same tree cc0-here uses).
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: http_capture_invoke.sh --mitm-port PORT --model MODEL [--work-dir DIR]

Environment:
  CC0_USER_OVERLAY   OAuth/config overlay (default ~/.cache/cc0/claude-user-overlay)
  CLAUDE_BIN         Claude CLI (default ~/.local/bin/claude)
  TOKENKEY_CC_HTTP_CAPTURE_PROMPT  override probe prompt (default: Say only the word PONG)
EOF
}

[[ -f "${HOME}/.config/cc0/env" ]] && # shellcheck disable=SC1091
  source "${HOME}/.config/cc0/env"

MITM_PORT=""
MODEL=""
WORK_DIR=""
PROMPT="${TOKENKEY_CC_HTTP_CAPTURE_PROMPT:-Say only the word PONG}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mitm-port) MITM_PORT="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --work-dir) WORK_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

if [[ -z "$MITM_PORT" || -z "$MODEL" ]]; then
  usage >&2
  exit 1
fi

CLAUDE_BIN="${CLAUDE_BIN:-$HOME/.local/bin/claude}"
if [[ ! -x "$CLAUDE_BIN" ]]; then
  CLAUDE_BIN="$(command -v claude || true)"  # preflight-allow: swallow
fi
if [[ -z "$CLAUDE_BIN" || ! -x "$CLAUDE_BIN" ]]; then
  echo "error: Claude CLI not found (set CLAUDE_BIN)" >&2
  exit 1
fi

OVERLAY="${CC0_USER_OVERLAY:-$HOME/.cache/cc0/claude-user-overlay}"
CA="${HOME}/.mitmproxy/mitmproxy-ca-cert.pem"
if [[ ! -f "$CA" ]]; then
  echo "error: mitm CA missing at $CA" >&2
  exit 1
fi

if [[ -z "$WORK_DIR" ]]; then
  WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tk-http-capture.XXXXXX")"
  _cleanup_work() { rm -rf "$WORK_DIR"; }
  trap _cleanup_work EXIT
fi
mkdir -p "$WORK_DIR"

PROXY="http://127.0.0.1:${MITM_PORT}"
TAG="${MODEL##*-}"
OUT="$WORK_DIR/claude-${TAG}.out"
ERR="$WORK_DIR/claude-${TAG}.err"

# Neutral cwd: avoid sub2api project SessionStart context short-circuiting /v1/messages.
set +e
(
  cd /tmp || exit 1
  env -i \
    HOME="$HOME" \
    USER="${USER:-$(id -un)}" \
    LOGNAME="${LOGNAME:-$(id -un)}" \
    PATH="$PATH" \
    SHELL="${SHELL:-/bin/sh}" \
    TERM="${TERM:-xterm-256color}" \
    LANG="${LANG:-en_US.UTF-8}" \
    CLAUDE_CONFIG_DIR="$OVERLAY" \
    HTTP_PROXY="$PROXY" \
    HTTPS_PROXY="$PROXY" \
    http_proxy="$PROXY" \
    https_proxy="$PROXY" \
    NO_PROXY="127.0.0.1,localhost,::1" \
    no_proxy="127.0.0.1,localhost,::1" \
    NODE_EXTRA_CA_CERTS="$CA" \
    NODE_TLS_REJECT_UNAUTHORIZED=0 \
    CLAUDE_CODE_REMOTE_SEND_KEEPALIVES=true \
    "$CLAUDE_BIN" \
    -p "$PROMPT" \
    --model "$MODEL" \
    --allowedTools '' \
    --max-budget-usd 0.15 \
    --output-format text \
    </dev/null >"$OUT" 2>"$ERR"
)
rc=$?
set -e

if [[ -s "$ERR" ]]; then
  sed -n '1,5p' "$ERR" >&2 || true  # preflight-allow: swallow
fi

exit "$rc"
