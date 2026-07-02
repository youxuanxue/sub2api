#!/usr/bin/env bash
# endpoint-compat-audit.sh - unified TokenKey endpoint compatibility audit entrypoint.
#
# This is an orchestrator over existing probes; it does not invent a third
# verdict vocabulary. Use direct route-gate matrix for platform/path gates, then
# universal full matrix for true end-to-end servability through a universal key.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${TK_ENDPOINT_AUDIT_TARGET:-prod}"
SKIP_PAID=0
WITH_EXTRAS=0
MODE="print"

usage() {
	cat <<'EOF'
Usage:
  bash ops/observability/endpoint-compat-audit.sh --print
  bash ops/observability/endpoint-compat-audit.sh --direct-route-gate
  bash ops/observability/endpoint-compat-audit.sh --universal-matrix [--skip-paid] [--with-extras]
  bash ops/observability/endpoint-compat-audit.sh --all [--skip-paid] [--with-extras]

Environment:
  TK_ENDPOINT_AUDIT_TARGET=prod|edge      target for run-probe.sh (default: prod)
  TK_FULLTEST_KEY=sk-...                  universal key for --universal-matrix/--all
  TK_FULLTEST_KIRO_KEY=sk-...             optional direct Kiro key for Kiro matrix row
  TK_FULLTEST_BASE_URL=https://...        optional base URL for universal matrix

Verdict split:
  direct-route-gate checks group.platform x endpoint local route gates on prod.
  universal-matrix checks real end-to-end servability through one universal key.
EOF
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--print) MODE="print" ;;
		--direct-route-gate) MODE="direct" ;;
		--universal-matrix) MODE="universal" ;;
		--all) MODE="all" ;;
		--skip-paid) SKIP_PAID=1 ;;
		--with-extras) WITH_EXTRAS=1 ;;
		-h|--help) usage; exit 0 ;;
		*) echo "unknown arg: $1" >&2; usage >&2; exit 2 ;;
	esac
	shift
done

direct_cmd=(
	bash "$ROOT/ops/observability/run-probe.sh"
	--target "$TARGET"
	--script "$ROOT/ops/observability/probe-endpoint-matrix.sh"
	--with "$ROOT/ops/pricing/probe_reserved_resources.sh"
)

universal_cmd=(bash "$ROOT/ops/test/gateway_full_matrix_test.sh")
if [[ "$SKIP_PAID" == "1" ]]; then
	universal_cmd+=(--skip-paid)
fi
if [[ "$WITH_EXTRAS" == "1" ]]; then
	universal_cmd+=(--with-extras)
fi

print_cmd() {
	printf '%q ' "$@"
	printf '\n'
}

case "$MODE" in
	print)
		echo "# Direct route-gate matrix"
		print_cmd "${direct_cmd[@]}"
		echo "# Universal full matrix"
		print_cmd "${universal_cmd[@]}"
		;;
	direct)
		exec "${direct_cmd[@]}"
		;;
	universal)
		exec "${universal_cmd[@]}"
		;;
	all)
		"${direct_cmd[@]}"
		"${universal_cmd[@]}"
		;;
esac
