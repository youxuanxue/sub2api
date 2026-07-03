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
assert_eq "$(tk_probe_canonical_scope endpoint_matrix_grok grok source_group_id 25)" "grok_srcgrp_25" "source group id canonical scope"
assert_eq "$(tk_probe_canonical_scope endpoint_matrix_anthropic anthropic group_id_like '1|cc-%')" "anthropic_srcgrp_1_cc" "group id like canonical scope"
assert_eq "$(tk_probe_canonical_scope openai_ct_acct_63 openai account_ids '63')" "openai_acct_63" "account id canonical scope"
TK_PROBE_LEGACY_SCOPE=1
assert_eq "$(tk_probe_canonical_scope endpoint_matrix_grok grok source_group_id 25)" "endpoint_matrix_grok" "legacy scope override"
unset TK_PROBE_LEGACY_SCOPE

TK_PROBE_TEST_SCENARIO=case_match
TK_PROBE_LAST_SQL=""
tk_probe_psql() {
	local sql=""
	while [ "$#" -gt 0 ]; do
		if [ "$1" = "-c" ]; then
			sql="$2"
			break
		fi
		shift
	done
	TK_PROBE_LAST_SQL="$sql"
	case "$TK_PROBE_TEST_SCENARIO" in
	case_match)
		if printf '%s' "$sql" | grep -q "SELECT COALESCE"; then
			printf 'Google-Vertex\n'
		else
			printf '1\n'
		fi
		;;
	ambiguous)
		if printf '%s' "$sql" | grep -q "SELECT COALESCE"; then
			printf '\n'
		else
			printf '2\n'
		fi
		;;
	group_id)
		if printf '%s' "$sql" | grep -q "SELECT COALESCE"; then
			printf '39\n'
		else
			printf '1\n'
		fi
		;;
	*)
		printf '\n'
		;;
	esac
}

assert_eq "$(tk_probe_resolve_source_group 'google-vertex' 2>/tmp/tk-probe-resolve.err)" "Google-Vertex" "source group case-insensitive fallback"

TK_PROBE_GROUP_ID=39
TK_PROBE_TEST_SCENARIO=case_match
tk_probe_bind_from_group_id probe 16
tk_probe_bind_from_group_id_like probe 1 'cc-%'
TK_PROBE_TEST_SCENARIO=group_id
tk_probe_clear_bindings probe
if ! printf '%s' "$TK_PROBE_LAST_SQL" | grep -q "status = 'disabled'"; then
	echo "FAIL: clear_bindings should disable reusable probe key/group" >&2
	exit 1
fi

TK_PROBE_TEST_SCENARIO=ambiguous
if tk_probe_resolve_source_group 'google-vertex' >/tmp/tk-probe-resolve.out 2>/tmp/tk-probe-resolve.err; then
	echo "FAIL: ambiguous source group should fail" >&2
	exit 1
fi

if tk_probe_bind_from_group_id probe Google-Vertex >/tmp/tk-probe-resolve.out 2>/tmp/tk-probe-resolve.err; then
	echo "FAIL: non-numeric source group id should fail" >&2
	exit 1
fi

if tk_probe_bind_from_group_id_like probe claude 'cc-%' >/tmp/tk-probe-resolve.out 2>/tmp/tk-probe-resolve.err; then
	echo "FAIL: non-numeric source group id for group_id_like should fail" >&2
	exit 1
fi

echo "test_probe_reserved_resources: PASS"
