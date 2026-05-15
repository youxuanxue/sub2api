#!/usr/bin/env bash
# Capture the local Claude Code CLI TLS fingerprint once per day using a real
# claude CLI request against the upstream TokenKey collector.
set -u

REPO_ROOT="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"
OUT_DIR="$REPO_ROOT/.tls_list"
COLLECTOR_ORIGIN="${TOKENKEY_TLS_PROFILE_COLLECTOR_ORIGIN:-https://tls.sub2api.org}"
COLLECTOR_API_ORIGIN="${TOKENKEY_TLS_PROFILE_COLLECTOR_API_ORIGIN:-https://tls.sub2api.org}"
MODEL="${TOKENKEY_TLS_PROFILE_CAPTURE_MODEL:-claude-haiku-4-5-20251001}"
TODAY="$(date -u +%Y%m%d)"
DAILY_MARKER="$OUT_DIR/$TODAY.claude-cli-captured.json"

emit() {
  printf '%s\n' "$1"
}

if [ "${CLAUDE_CODE_REMOTE:-}" = "true" ] || [ "${TOKENKEY_TLS_PROFILE_CAPTURE_ACTIVE:-}" = "1" ]; then
  emit '{"suppressOutput":true}'
  exit 0
fi

if ! command -v claude >/dev/null 2>&1; then
  emit '{"systemMessage":"TokenKey TLS profile capture skipped: claude CLI is not available","suppressOutput":false}'
  exit 0
fi
if ! command -v jq >/dev/null 2>&1; then
  emit '{"systemMessage":"TokenKey TLS profile capture skipped: jq is not available","suppressOutput":false}'
  exit 0
fi
if ! command -v curl >/dev/null 2>&1; then
  emit '{"systemMessage":"TokenKey TLS profile capture skipped: curl is not available","suppressOutput":false}'
  exit 0
fi

mkdir -p "$OUT_DIR"
if [ -f "$DAILY_MARKER" ]; then
  emit '{"suppressOutput":true}'
  exit 0
fi

HOOK_INPUT="$(cat || true)"
SESSION_ID="$(printf '%s' "$HOOK_INPUT" | jq -r '.session_id // empty' 2>/dev/null || true)"
SESSION_SHORT="$(printf '%s' "${SESSION_ID:-session}" | tr -cd '[:alnum:]_-' | cut -c1-12)"
[ -n "$SESSION_SHORT" ] || SESSION_SHORT="session"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
TOKEN="tk-claude-tls-$STAMP-$SESSION_SHORT-$$"
CAPTURE_WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/tk-claude-tls.XXXXXX")"
cleanup() { rm -rf "$CAPTURE_WORKDIR"; }
trap cleanup EXIT

# Claude Code 2.1.x honors CLAUDE_CODE_API_BASE_URL for API endpoint override.
# ANTHROPIC_BASE_URL is kept for older/newer variants that may still read it.
CLAUDE_OUTPUT="$CAPTURE_WORKDIR/claude.out"
env -i \
  PATH="$PATH" \
  HOME="$CAPTURE_WORKDIR/home" \
  TERM="${TERM:-xterm}" \
  SHELL="${SHELL:-/bin/sh}" \
  ANTHROPIC_CONFIG_DIR="$CAPTURE_WORKDIR/anthropic" \
  CLAUDE_CONFIG_DIR="$CAPTURE_WORKDIR/claude" \
  CLAUDE_CODE_API_BASE_URL="$COLLECTOR_ORIGIN:8090" \
  ANTHROPIC_BASE_URL="$COLLECTOR_ORIGIN:8090" \
  ANTHROPIC_API_KEY="$TOKEN" \
  ANTHROPIC_AUTH_TOKEN="$TOKEN" \
  TOKENKEY_TLS_PROFILE_CAPTURE_ACTIVE=1 \
  NODE_TLS_REJECT_UNAUTHORIZED=0 \
  claude --bare -p 'test' --model "$MODEL" --allowedTools '' --max-budget-usd 1 \
  >"$CLAUDE_OUTPUT" 2>&1 || true

LATEST_URL="$COLLECTOR_API_ORIGIN/api/latest?token=$TOKEN"
LATEST_JSON="$CAPTURE_WORKDIR/latest.json"
if ! curl -fsS --max-time 20 "$LATEST_URL" > "$LATEST_JSON"; then
  emit '{"systemMessage":"TokenKey TLS profile capture failed: collector latest API request failed","suppressOutput":false}'
  exit 0
fi

COUNT="$(jq -r '.count // 0' "$LATEST_JSON")"
if [ "${COUNT:-0}" = "0" ]; then
  SUMMARY="$(tr '\n' ' ' < "$CLAUDE_OUTPUT" | cut -c1-240)"
  emit "$(jq -cn --arg msg "TokenKey TLS profile capture failed: collector recorded no Claude CLI fingerprint. claude_output=$SUMMARY" '{systemMessage:$msg,suppressOutput:false}')"
  exit 0
fi

FIRST_JSON="$CAPTURE_WORKDIR/first.json"
jq '.fingerprints[0]' "$LATEST_JSON" > "$FIRST_JSON"
JA3_HASH="$(jq -r '.ja3_hash // "noja3hash"' "$FIRST_JSON")"
SHORT_JA3="$(printf '%s' "$JA3_HASH" | tr -cd '[:xdigit:]' | cut -c1-12)"
[ -n "$SHORT_JA3" ] || SHORT_JA3="noja3hash"
PROFILE_NAME="claude_cli_${STAMP}_${SHORT_JA3}"
BASE="$STAMP-$SESSION_SHORT-$SHORT_JA3"
CAPTURE_PATH="$OUT_DIR/$BASE.capture.json"
PROFILE_PATH="$OUT_DIR/$BASE.tokenkey-profile.json"
YAML_PATH="$OUT_DIR/$BASE.tokenkey-profile.yaml"

jq -n \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg collector "$COLLECTOR_ORIGIN:8090" \
  --arg api "$COLLECTOR_API_ORIGIN/api/latest" \
  --arg event "SessionStart" \
  --arg session_id "$SESSION_ID" \
  --slurpfile latest "$LATEST_JSON" \
  --slurpfile first "$FIRST_JSON" \
  '{
    schema_version: 2,
    captured_at: $captured_at,
    collector: {url: $collector, latest_api: $api},
    claude_hook: {event: $event, session_id: ($session_id | if . == "" then null else . end)},
    latest: $latest[0],
    observed: $first[0]
  }' > "$CAPTURE_PATH"

jq -n \
  --arg name "$PROFILE_NAME" \
  --arg desc "Captured from real Claude Code CLI request to $COLLECTOR_ORIGIN:8090 at $STAMP. ja3_hash=$JA3_HASH." \
  --slurpfile fp "$FIRST_JSON" \
  '{
    name: $name,
    description: $desc,
    enable_grease: ($fp[0].enable_grease // false),
    cipher_suites: ($fp[0].cipher_suites // []),
    curves: ($fp[0].curves // []),
    point_formats: ($fp[0].point_formats // []),
    signature_algorithms: ($fp[0].signature_algorithms // []),
    alpn_protocols: ($fp[0].alpn_protocols // []),
    supported_versions: ($fp[0].supported_versions // []),
    key_share_groups: ($fp[0].key_share_groups // []),
    psk_modes: ($fp[0].psk_modes // []),
    extensions: ($fp[0].extensions // []),
    observed: {
      model: $fp[0].model,
      user_agent: $fp[0].user_agent,
      stainless_os: $fp[0].stainless_os,
      stainless_arch: $fp[0].stainless_arch,
      stainless_runtime: $fp[0].stainless_runtime,
      stainless_runtime_version: $fp[0].stainless_runtime_version,
      stainless_lang: $fp[0].stainless_lang,
      stainless_package_version: $fp[0].stainless_package_version,
      ja3_raw: $fp[0].ja3_raw,
      ja3_hash: $fp[0].ja3_hash,
      ja4: $fp[0].ja4
    }
  }' > "$PROFILE_PATH"

python3 - "$PROFILE_PATH" "$YAML_PATH" <<'PY'
import json
import sys
profile_path, yaml_path = sys.argv[1:]
p = json.load(open(profile_path, encoding='utf-8'))

def fmt(v):
    if isinstance(v, str):
        return json.dumps(v)
    return str(v).lower() if isinstance(v, bool) else str(v)

lines = [
    '# TokenKey TLS Fingerprint Profile sample captured by real Claude Code CLI',
    f'{p["name"]}:',
    f'  name: {json.dumps(p["name"])}',
    f'  description: {json.dumps(p.get("description", ""))}',
    f'  enable_grease: {str(bool(p.get("enable_grease"))).lower()}',
]
for key in [
    'cipher_suites', 'curves', 'point_formats', 'signature_algorithms',
    'alpn_protocols', 'supported_versions', 'key_share_groups', 'psk_modes', 'extensions',
]:
    arr = p.get(key) or []
    lines.append(f'  {key}: [{", ".join(fmt(x) for x in arr)}]')
lines.append('')
open(yaml_path, 'w', encoding='utf-8').write('\n'.join(lines))
PY

jq -n \
  --arg date "$TODAY" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg session_id "$SESSION_ID" \
  --arg profile_path "$(basename "$PROFILE_PATH")" \
  --arg capture_path "$(basename "$CAPTURE_PATH")" \
  '{
    schema_version: 1,
    date: $date,
    captured_at: $captured_at,
    session_id: ($session_id | if . == "" then null else . end),
    profile_path: $profile_path,
    capture_path: $capture_path
  }' > "$DAILY_MARKER"

emit "$(jq -cn --arg msg "TokenKey TLS profile captured: ${PROFILE_PATH#$REPO_ROOT/}" '{systemMessage:$msg,suppressOutput:true}')"
