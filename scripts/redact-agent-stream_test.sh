#!/usr/bin/env bash
#
# Self-test for scripts/redact-agent-stream.py.
# Runs as the last preflight check; fails the commit if redaction breaks.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/redact-agent-stream.py"

if [ ! -x "$SCRIPT" ]; then
  echo "FAIL: $SCRIPT missing or not executable" >&2
  exit 1
fi

pass=0
fail=0
expect() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    echo "FAIL: $label" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
  fi
}

# 1) Exact env-listed secret replaced
out="$(printf 'auth: sk-abcdef0123456789xyz tail\n' \
  | ANTHROPIC_AUTH_TOKEN="sk-abcdef0123456789xyz" python3 "$SCRIPT")"
expect "env-exact (ANTHROPIC_AUTH_TOKEN)" "auth: ***REDACTED*** tail" "$out"

# 2) GH PAT format pattern catches even when not in env
out="$(printf 'token: ghp_abcdefghijklmnopqrstuvwxyz12345 tail\n' | python3 "$SCRIPT")"
expect "ghp_ pattern" "token: ***REDACTED*** tail" "$out"

# 3) sk- pattern catches even when not in env
out="$(printf 'key: sk-leakedABCDEFG1234567890 tail\n' | python3 "$SCRIPT")"
expect "sk- pattern" "key: ***REDACTED*** tail" "$out"

# 4) github_pat_ format pattern
out="$(printf 'pat: github_pat_AAA1234567890BCDEFGHIJ tail\n' | python3 "$SCRIPT")"
expect "github_pat_ pattern" "pat: ***REDACTED*** tail" "$out"

# 5) Non-secret content passes through unchanged
in_line="just a normal log line with no secrets"
out="$(printf '%s\n' "$in_line" | python3 "$SCRIPT")"
expect "passthrough" "$in_line" "$out"

# 6) Two secrets on one line both replaced
out="$(printf 'two: sk-leakedABCDEFG1234567890 and ghp_abcdefghijklmnopqrstuvwxyz12345\n' \
  | python3 "$SCRIPT")"
expect "multi-secret" "two: ***REDACTED*** and ***REDACTED***" "$out"

# 7) Short env value (< 8) is ignored to avoid over-redaction
out="$(printf 'short: ab tail\n' \
  | ANTHROPIC_AUTH_TOKEN="ab" python3 "$SCRIPT")"
expect "short-value guard" "short: ab tail" "$out"

# 8) Custom REDACT_FROM_ENV override (allows project-specific extension
#     without editing the script)
out="$(printf 'custom: my-custom-secret-value tail\n' \
  | REDACT_FROM_ENV=MY_SECRET MY_SECRET="my-custom-secret-value" python3 "$SCRIPT")"
expect "REDACT_FROM_ENV override" "custom: ***REDACTED*** tail" "$out"

# 9) Multi-line input, each line independently filtered
out="$(printf 'l1: sk-leakedABCDEFG1234567890\nl2: clean line\n' | python3 "$SCRIPT")"
expected=$'l1: ***REDACTED***\nl2: clean line'
expect "multi-line" "$expected" "$out"

# 10) Regression: short github_pat_ prefix (run 25419117708 leaked
#     `github_pat_11ABMYKSY`, 9 chars after prefix). Previous {20,}
#     regex missed this; new pattern accepts any non-empty suffix.
out="$(printf 'pat-short: github_pat_11ABMYKSY trailing\n' | python3 "$SCRIPT")"
expect "regression: short github_pat_ (9 chars)" "pat-short: ***REDACTED*** trailing" "$out"

# 11) Regression: agent text mentioning truncated PAT with literal "..."
#     marker (this was the exact shape that leaked).
out="$(printf 'token: github_pat_11ABMYKSY0... bare suffix\n' | python3 "$SCRIPT")"
# regex stops at the first non-[A-Za-z0-9_] char (the "."), so the trailing
# "..." literal stays in place — that is intentional (signals truncation).
expect "regression: github_pat_ with truncation marker" "token: ***REDACTED***... bare suffix" "$out"

# 12) Bare github_pat_ prefix (no suffix) is also redacted, since the
#     prefix itself is distinctive enough to be suspicious in agent
#     output. Trade-off: documentation that mentions the literal prefix
#     (e.g. "fine-grained github_pat_ format") gets redacted too. We
#     accept that — agent stdout is not documentation.
out="$(printf 'doc-ref: github_pat_ here\n' | python3 "$SCRIPT")"
expect "bare github_pat_ prefix" "doc-ref: ***REDACTED*** here" "$out"

# 13) Short ghp_ token (e.g. `ghp_abc123`) is now redacted too.
out="$(printf 'gh-short: ghp_abc123def trailing\n' | python3 "$SCRIPT")"
expect "regression: short ghp_" "gh-short: ***REDACTED*** trailing" "$out"

# 14) sk- keeps {8,} floor — strings shorter than 8 chars after the
#     prefix are NOT redacted (avoids over-redacting "sk-test" type
#     fixtures). 8+ chars: redacted.
out="$(printf 'sk-len-7: sk-abc123 / sk-len-8: sk-abc12345 tail\n' | python3 "$SCRIPT")"
expect "sk- threshold (7 chars passes, 8+ redacted)" "sk-len-7: sk-abc123 / sk-len-8: ***REDACTED*** tail" "$out"

if [ "$fail" -ne 0 ]; then
  echo "redact-agent-stream self-test: $pass passed, $fail FAILED" >&2
  exit 1
fi
echo "ok: redact-agent-stream self-test ($pass cases)"
