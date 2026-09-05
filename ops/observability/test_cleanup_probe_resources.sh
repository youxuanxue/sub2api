#!/usr/bin/env bash
# Static checks for cleanup-probe-resources.sh shared lifecycle delegation.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$ROOT/cleanup-probe-resources.sh"

text="$(cat "$SCRIPT")"

if ! grep -q 'tk_probe_cleanup_snapshot "$LIMIT" current' <<<"$text"; then
	echo "FAIL: cleanup script should delegate dry-run snapshot to probe_reserved_resources" >&2
	exit 1
fi
if ! grep -q 'tk_probe_cleanup_all_reserved' <<<"$text"; then
	echo "FAIL: cleanup script should delegate apply cleanup to probe_reserved_resources" >&2
	exit 1
fi
if ! grep -q 'probe_reserved_resources.sh' <<<"$text"; then
	echo "FAIL: cleanup script should source probe_reserved_resources.sh" >&2
	exit 1
fi
if ! grep -q 'PRICING_LIB="$SCRIPT_DIR/probe_reserved_resources.sh"' <<<"$text"; then
	echo "FAIL: cleanup script should fall back to /tmp companion path for run-probe delivery" >&2
	exit 1
fi

echo "test_cleanup_probe_resources: PASS"
