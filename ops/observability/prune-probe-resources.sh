#!/usr/bin/env bash
# Soft-delete legacy __tk_probe_* rows that are superseded by canonical reusable scopes.
# Canonical keep/prune rules live in the shared probe owner: ops/pricing/probe_reserved_resources.sh.
set -euo pipefail

APPLY="${TK_PROBE_PRUNE_APPLY:-0}"

usage() {
	cat <<'USAGE'
Usage:
  bash ops/observability/prune-probe-resources.sh [--apply]

Default is dry-run. --apply soft-deletes non-canonical __tk_probe_* api_keys/groups.
USAGE
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--apply)
		APPLY=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "prune-probe-resources: unknown arg: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRICING_LIB="$SCRIPT_DIR/../pricing/probe_reserved_resources.sh"
if [ ! -f "$PRICING_LIB" ]; then
	PRICING_LIB="$SCRIPT_DIR/probe_reserved_resources.sh"
fi
if [ ! -f "$PRICING_LIB" ]; then
	echo "prune-probe-resources: missing probe_reserved_resources.sh (use run-probe --with ops/pricing/probe_reserved_resources.sh)" >&2
	exit 1
fi
# shellcheck source=../pricing/probe_reserved_resources.sh
. "$PRICING_LIB"

printf 'mode=%s\n' "$([ "$APPLY" = 1 ] && echo apply || echo dry_run)"
tk_probe_prune_report current
if [ "$APPLY" = 1 ]; then
	tk_probe_prune_noncanonical
	tk_probe_prune_report after
fi
