#!/usr/bin/env bash
# Deterministic unit checks for probe_reserved_resources.sh (no prod DB).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=probe_reserved_resources.sh
. "$ROOT/probe_reserved_resources.sh"

assert_eq() {
	if [ "$1" != "$2" ]; then
		echo "FAIL: expected '$2' got '$1' ($3)" >&2
		exit 1
	fi
}

assert_eq "$(tk_probe_scope_from_platform 'OpenAI')" "openai" "platform scope"
assert_eq "$(tk_probe_scope_from_platform 'newapi')" "newapi" "newapi scope"
assert_eq "$(tk_probe_group_name 'openai')" "__tk_probe_openai_group" "group name"
assert_eq "$(tk_probe_key_name 'newapi_google')" "__tk_probe_newapi_google_key" "key name"
assert_eq "$(tk_probe_reuse_lock_path 'anthropic')" "/tmp/tokenkey-account-model-probe-anthropic.lock" "reuse lock path"
assert_eq "$(tk_probe_sql_escape "it's")" "it''s" "sql escape"

echo "test_probe_reserved_resources: PASS"
