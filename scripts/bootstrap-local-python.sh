#!/usr/bin/env bash
# Bootstrap a repo-local Python 3.12+ venv for preflight / ops scripts.
#
# macOS ships python3 3.9 without datetime.UTC and usually without PyYAML.
# CI uses actions/setup-python 3.x and is unaffected.
#
# Usage:
#   bash scripts/bootstrap-local-python.sh          # create .venv + install deps
#   bash scripts/bootstrap-local-python.sh --check # verify .venv is usable
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VENV="$REPO_ROOT/.venv"
REQ="$REPO_ROOT/requirements-preflight.txt"
WRAPPER="$HOME/.local/bin/python3"

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "bootstrap-local-python: missing $1 on PATH" >&2
    exit 1
  }
}

check_venv() {
  if [[ ! -x "$VENV/bin/python" ]]; then
    echo "bootstrap-local-python: .venv missing — run without --check first" >&2
    return 1
  fi
  "$VENV/bin/python" - <<'PY'
import datetime as dt
import yaml

assert hasattr(dt, "UTC")
print(yaml.__version__)
PY
}

if [[ "${1:-}" == "--check" ]]; then
  check_venv >/dev/null
  echo "ok: .venv python usable (PyYAML + datetime.UTC)"
  exit 0
fi

need_cmd uv
[[ -f "$REQ" ]] || { echo "bootstrap-local-python: missing $REQ" >&2; exit 1; }

echo "bootstrap-local-python: creating $VENV (python 3.12+)"
uv venv "$VENV" --python 3.12
uv pip install -p "$VENV" -r "$REQ"

check_venv >/dev/null

mkdir -p "$(dirname "$WRAPPER")"
cat >"$WRAPPER" <<EOF
#!/bin/sh
exec $VENV/bin/python "\$@"
EOF
chmod +x "$WRAPPER"

echo "bootstrap-local-python: wrote $WRAPPER -> $VENV/bin/python"
echo "bootstrap-local-python: verify with: python3 --version && python3 -c 'import yaml'"

if command -v go >/dev/null 2>&1; then
  _goproxy="$(go env GOPROXY 2>/dev/null || true)"
  if [[ "$_goproxy" == *"proxy.golang.org"* && "$_goproxy" != *"goproxy.cn"* ]]; then
    echo "bootstrap-local-python: hint — if 'go generate ./ent' hangs on module download,"
    echo "  run: go env -w GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct"
  fi
fi
