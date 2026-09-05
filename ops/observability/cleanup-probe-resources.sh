#!/usr/bin/env bash
# Disable reserved __tk_probe_* resources after diagnostics so Studio key/group
# pickers are not dominated by probe-only fixtures. Lifecycle rules live in the
# shared probe owner: ops/pricing/probe_reserved_resources.sh.
set -euo pipefail

APPLY="${TK_PROBE_CLEANUP_APPLY:-0}"
LIMIT="${TK_PROBE_CLEANUP_LIMIT:-40}"
PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

usage() {
	cat <<'USAGE'
Usage:
  bash ops/observability/cleanup-probe-resources.sh [--apply] [--limit N]

Default is dry-run. --apply deletes account_groups bindings for __tk_probe_*
groups and disables matching probe groups/API keys; it never deletes rows.
USAGE
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--apply)
		APPLY=1
		shift
		;;
	--limit)
		LIMIT="${2:-}"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "cleanup-probe-resources: unknown arg: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

if ! [[ "$LIMIT" =~ ^[0-9]+$ ]] || [ "$LIMIT" -lt 1 ]; then
	echo "cleanup-probe-resources: --limit must be a positive integer" >&2
	exit 1
fi
if [[ "$APPLY" != "0" && "$APPLY" != "1" ]]; then
	echo "cleanup-probe-resources: TK_PROBE_CLEANUP_APPLY must be 0 or 1" >&2
	exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRICING_LIB="$SCRIPT_DIR/../pricing/probe_reserved_resources.sh"
if [ ! -f "$PRICING_LIB" ]; then
	PRICING_LIB="$SCRIPT_DIR/probe_reserved_resources.sh"
fi
if [ ! -f "$PRICING_LIB" ]; then
	echo "cleanup-probe-resources: missing probe_reserved_resources.sh (use run-probe --with ops/pricing/probe_reserved_resources.sh)" >&2
	exit 1
fi
# shellcheck source=../pricing/probe_reserved_resources.sh
. "$PRICING_LIB"

if [ "$APPLY" = "1" ]; then
	printf 'mode=apply\n'
	tk_probe_cleanup_snapshot "$LIMIT" before
	tk_probe_cleanup_all_reserved
	tk_probe_cleanup_snapshot "$LIMIT" after
else
	printf 'mode=dry_run\n'
	tk_probe_cleanup_snapshot "$LIMIT" current
fi
