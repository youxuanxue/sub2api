#!/usr/bin/env bash
# Fixture tests for sync-control-alias-on-host.sh against a fake docker.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/sync-control-alias-on-host.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
failures=0

mkdir -p "$WORK/bin"
cat >"$WORK/bin/docker" <<'EOS'
#!/usr/bin/env bash
exists() { grep -qxF "$1" "$STATE/containers" 2>/dev/null; }
cmd="$1"; shift
case "$cmd" in
  inspect)
    target="$1"; shift
    exists "$target" || { echo "No such object: $target" >&2; exit 1; }
    fmt=""; prev=""
    for a in "$@"; do [ "$prev" = "--format" ] && fmt="$a"; prev="$a"; done
    case "$fmt" in
      *Image*) cat "$STATE/image-$target" 2>/dev/null || echo "alpine/socat:1.8.0.0" ;;
      *Cmd*) cat "$STATE/cmd-$target" 2>/dev/null || echo '["TCP-LISTEN:8080,fork,reuseaddr","TCP:tokenkey-blue:8080"]' ;;
      *Running*) cat "$STATE/running-$target" 2>/dev/null || echo true ;;
    esac
    ;;
  image) exit 0 ;;
  pull) echo "pull $*" >>"$STATE/actions" ;;
  rm)
    echo "rm $*" >>"$STATE/actions"
    name="${!#}"
    { grep -vxF "$name" "$STATE/containers" || true; } >"$STATE/containers.tmp"
    mv "$STATE/containers.tmp" "$STATE/containers"
    ;;
  run)
    echo "run $*" >>"$STATE/actions"
    name=""; prev=""
    for a in "$@"; do [ "$prev" = "--name" ] && name="$a"; prev="$a"; done
    echo "$name" >>"$STATE/containers"
    echo fakeid
    ;;
  exec) echo "exec $*" >>"$STATE/actions"; exit "${TEST_EXEC_STATUS:-0}" ;;
  *) echo "unexpected docker $cmd $*" >&2; exit 1 ;;
esac
EOS
chmod +x "$WORK/bin/docker" "$TARGET"

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; echo "$2"; failures=$((failures + 1)); }

# 1) Create proxy for blue.
STATE="$WORK/s1"; mkdir -p "$STATE"; printf 'tokenkey-blue\n' >"$STATE/containers"; : >"$STATE/actions"
out="$(STATE="$STATE" PATH="$WORK/bin:$PATH" TK_DOCKER=docker TK_ACTIVE_COLOR=blue \
  TK_NETWORK=net bash "$TARGET" 2>&1)" || true
grep -q 'tk_gemini_web_control_alias: OK color=blue' <<<"$out" && grep -q 'run .*--name tokenkey' "$STATE/actions" \
  && pass create || fail create "$out / $(cat "$STATE/actions")"

# 2) Already correct: no rm/run.
STATE="$WORK/s2"; mkdir -p "$STATE"
printf 'tokenkey-blue\ntokenkey\n' >"$STATE/containers"
printf 'alpine/socat:1.8.0.0\n' >"$STATE/image-tokenkey"
printf '["TCP-LISTEN:8080,fork,reuseaddr","TCP:tokenkey-blue:8080"]\n' >"$STATE/cmd-tokenkey"
: >"$STATE/actions"
out="$(STATE="$STATE" PATH="$WORK/bin:$PATH" TK_DOCKER=docker TK_ACTIVE_COLOR=blue \
  TK_NETWORK=net bash "$TARGET" 2>&1)" || true
grep -q 'already proxies' <<<"$out" && ! grep -q '^rm ' "$STATE/actions" \
  && pass noop || fail noop "$out / $(cat "$STATE/actions")"

# 3) Legacy app container named tokenkey.
STATE="$WORK/s3"; mkdir -p "$STATE"
printf 'tokenkey-blue\ntokenkey\n' >"$STATE/containers"
printf 'ghcr.io/youxuanxue/sub2api:1.8.284\n' >"$STATE/image-tokenkey"
: >"$STATE/actions"
out="$(STATE="$STATE" PATH="$WORK/bin:$PATH" TK_DOCKER=docker TK_ACTIVE_COLOR=blue \
  TK_NETWORK=net bash "$TARGET" 2>&1)" || true
grep -q 'legacy app container' <<<"$out" && ! grep -q '^rm ' "$STATE/actions" \
  && pass legacy || fail legacy "$out / $(cat "$STATE/actions")"

# 4) Switch blue→green proxy target.
STATE="$WORK/s4"; mkdir -p "$STATE"
printf 'tokenkey-green\ntokenkey\n' >"$STATE/containers"
printf 'alpine/socat:1.8.0.0\n' >"$STATE/image-tokenkey"
printf '["TCP-LISTEN:8080,fork,reuseaddr","TCP:tokenkey-blue:8080"]\n' >"$STATE/cmd-tokenkey"
: >"$STATE/actions"
out="$(STATE="$STATE" PATH="$WORK/bin:$PATH" TK_DOCKER=docker TK_ACTIVE_COLOR=green \
  TK_NETWORK=net bash "$TARGET" 2>&1)" || true
grep -q 'target=tokenkey-green' <<<"$out" && grep -q '^rm ' "$STATE/actions" \
  && grep -q 'TCP:tokenkey-green:8080' "$STATE/actions" \
  && pass switch || fail switch "$out / $(cat "$STATE/actions")"

# 5) Caddyfile fallback when TK_ACTIVE_COLOR and active-color file are absent.
STATE="$WORK/s5"; mkdir -p "$STATE"
printf 'tokenkey-green\n' >"$STATE/containers"
: >"$STATE/actions"
printf 'reverse_proxy tokenkey-green:8080\n' >"$WORK/Caddyfile"
out="$(STATE="$STATE" PATH="$WORK/bin:$PATH" TK_DOCKER=docker \
  TK_ACTIVE_COLOR= TK_ACTIVE_COLOR_FILE="$WORK/missing-active" \
  TK_CADDYFILE="$WORK/Caddyfile" TK_NETWORK=net bash "$TARGET" 2>&1)" || true
grep -q 'tk_gemini_web_control_alias: OK color=green' <<<"$out" \
  && grep -q 'TCP:tokenkey-green:8080' "$STATE/actions" \
  && pass caddyfile || fail caddyfile "$out / $(cat "$STATE/actions")"

# A fresh worker is stopped until its control API is reachable. Probe through
# the active gateway instead; a failed HTTP probe must fail alias preparation.
for expected in 0 7; do
  STATE="$WORK/stopped-$expected"; mkdir -p "$STATE"
  printf 'tokenkey-blue\ntokenkey-gemini-web\n' >"$STATE/containers"
  echo false >"$STATE/running-tokenkey-gemini-web"
  : >"$STATE/actions"
  status=0
  out="$(STATE="$STATE" PATH="$WORK/bin:$PATH" TK_DOCKER=docker TK_ACTIVE_COLOR=blue \
    TK_NETWORK=net TEST_EXEC_STATUS="$expected" bash "$TARGET" 2>&1)" || status=$?
  [[ "$status" = "$expected" ]] && grep -q '^exec tokenkey-blue wget .*http://tokenkey:8080/health' "$STATE/actions" \
    && ! grep -q '^exec tokenkey-gemini-web ' "$STATE/actions" \
    && pass "stopped-worker-$expected" || fail "stopped-worker-$expected" "$out"
done

if [[ "$failures" -ne 0 ]]; then
  echo "test_sync_control_alias_on_host: FAIL ($failures)"
  exit 1
fi
echo "test_sync_control_alias_on_host: PASS"
