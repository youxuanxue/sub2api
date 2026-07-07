#!/usr/bin/env bash
# Static checks for prune-probe-resources.sh canonical keep set.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRUNE="$ROOT/prune-probe-resources.sh"
RESOURCES="$ROOT/../pricing/probe_reserved_resources.sh"

# shellcheck source=../pricing/probe_reserved_resources.sh
. "$RESOURCES"

text="$(cat "$PRUNE")"

while IFS= read -r scope; do
	[ -z "$scope" ] && continue
	if ! grep -q "$scope" <<<"$text"; then
		echo "FAIL: prune script missing platform reuse scope from probe_reserved_resources: ${scope}" >&2
		exit 1
	fi
done < <(tk_probe_platform_reuse_scopes)

if ! grep -q 'prune_candidates_legacy_tkprobe_groups' <<<"$text"; then
	echo "FAIL: prune script should report legacy tkprobe group candidates" >&2
	exit 1
fi
if ! grep -q 'stale_probe_bindings' <<<"$text"; then
	echo "FAIL: prune script should report stale probe account_groups bindings" >&2
	exit 1
fi
if ! grep -q 'probe_reserved_resources.sh' <<<"$text"; then
	echo "FAIL: prune script should source probe_reserved_resources.sh for shared scope list" >&2
	exit 1
fi
if ! grep -q 'PRICING_LIB="$SCRIPT_DIR/probe_reserved_resources.sh"' <<<"$text"; then
	echo "FAIL: prune script should fall back to /tmp companion path for run-probe delivery" >&2
	exit 1
fi

echo "test_prune_probe_resources: PASS"
