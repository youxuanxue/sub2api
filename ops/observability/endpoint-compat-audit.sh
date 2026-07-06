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
SSOT_SUBCOMMAND="list"
SSOT_ARGS=()
GATE_SHARDED=0
GATE_DEPLOY_CLOSEOUT=0
GATE_DEPLOY_CANARY=0
SKIP_RECENT_FILE="${TK_SSOT_SKIP_RECENT_FILE:-}"
GATE_SHARD_PLATFORMS=()
GATE_SHARD_SLEEP="${TK_SSOT_GATE_SHARD_SLEEP_SEC:-8}"

usage() {
	cat <<'EOF'
Usage:
  bash ops/observability/endpoint-compat-audit.sh --print
  bash ops/observability/endpoint-compat-audit.sh --direct-route-gate
  bash ops/observability/endpoint-compat-audit.sh --universal-matrix [--skip-paid] [--with-extras]
  bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix [--list|--run|--gate] [--include-paid] [--show-excluded] [--limit N]
  bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate-sharded [--include-paid] [--show-excluded]
  bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate-sharded --deploy-closeout [--include-paid] [--show-excluded]
  bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --deploy-canary --deploy-closeout
  bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate-sharded --skip-recent-file /path/to/recent.tsv
  bash ops/observability/endpoint-compat-audit.sh --all [--skip-paid] [--with-extras]

Environment:
  TK_ENDPOINT_AUDIT_TARGET=prod|edge      target for run-probe.sh (default: prod)
  TK_FULLTEST_KEY=sk-...                  universal key for --universal-matrix/--all
  TK_FULLTEST_KIRO_KEY=sk-...             optional direct Kiro key for Kiro matrix row
  TK_FULLTEST_BASE_URL=https://...        optional base URL for universal matrix

Verdict split:
  direct-route-gate checks group.platform x endpoint local route gates on prod.
  universal-matrix checks real end-to-end servability through one universal key.
  ssot-model-matrix derives model/protocol rows from live /api/v1/public/pricing.
  ssot-model-matrix --gate fails unless displayed+priced rows in scope pass live probes.
  ssot-model-matrix --gate-sharded runs the gate once per platform (manual/ad hoc only; not scheduled — account-ban risk).
  ssot-model-matrix --gate --deploy-canary probes one golden path per platform for deploy closeout.
  Catalog PRs: python3 scripts/checks/ssot-delta-gate.py check --base origin/main (CI job; delta models only).
  --skip-recent-file skips (model, modality) rows with recent successful usage_logs evidence (ad hoc sharded runs).
EOF
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--print) MODE="print" ;;
		--direct-route-gate) MODE="direct" ;;
		--universal-matrix) MODE="universal" ;;
		--ssot-model-matrix) MODE="ssot" ;;
		--all) MODE="all" ;;
		--skip-paid) SKIP_PAID=1 ;;
		--include-paid) SSOT_ARGS+=(--include-paid) ;;
		--with-extras) WITH_EXTRAS=1 ;;
		--list) SSOT_SUBCOMMAND="list" ;;
		--run) SSOT_SUBCOMMAND="run" ;;
		--gate) SSOT_SUBCOMMAND="gate" ;;
		--gate-sharded) MODE="ssot"; SSOT_SUBCOMMAND="gate"; GATE_SHARDED=1 ;;
		--deploy-closeout) GATE_DEPLOY_CLOSEOUT=1 ;;
		--deploy-canary) MODE="ssot"; SSOT_SUBCOMMAND="gate"; GATE_DEPLOY_CANARY=1 ;;
		--skip-recent-file)
			[[ $# -ge 2 ]] || { echo "$1 requires a value" >&2; usage >&2; exit 2; }
			SKIP_RECENT_FILE="$2"
			shift
			;;
		--show-excluded) SSOT_ARGS+=(--show-excluded) ;;
		--show-nonblocking-excluded) SSOT_ARGS+=(--show-nonblocking-excluded) ;;
		--json) SSOT_ARGS+=(--format json) ;;
		--limit|--only-platform|--only-protocol|--model|--base-url|--timeout)
			[[ $# -ge 2 ]] || { echo "$1 requires a value" >&2; usage >&2; exit 2; }
			SSOT_ARGS+=("$1" "$2")
			shift
			;;
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
ssot_cmd=(python3 "$ROOT/ops/test/gateway_model_ssot_matrix.py" "$SSOT_SUBCOMMAND")
if ((${#SSOT_ARGS[@]} > 0)); then
	ssot_cmd+=("${SSOT_ARGS[@]}")
fi

print_cmd() {
	printf '%q ' "$@"
	printf '\n'
}

load_gate_shard_platforms() {
	GATE_SHARD_PLATFORMS=()
	while IFS= read -r platform; do
		[[ -n "$platform" ]] && GATE_SHARD_PLATFORMS+=("$platform")
	done < <(python3 "$ROOT/ops/test/gateway_model_ssot_matrix.py" platforms)
	if ((${#GATE_SHARD_PLATFORMS[@]} == 0)); then
		echo "ERROR: no SSOT gate shard platforms reported by gateway_model_ssot_matrix.py" >&2
		exit 2
	fi
}

case "$MODE" in
	print)
		echo "# Direct route-gate matrix"
		print_cmd "${direct_cmd[@]}"
		echo "# Universal full matrix"
		print_cmd "${universal_cmd[@]}"
		echo "# SSOT-derived model/protocol matrix"
		print_cmd "${ssot_cmd[@]}"
		;;
	direct)
		exec "${direct_cmd[@]}"
		;;
	universal)
		exec "${universal_cmd[@]}"
		;;
	ssot)
		if [[ "$GATE_SHARDED" == "1" ]]; then
			if [[ -z "${TK_FULLTEST_KEY:-}" ]]; then
				echo "ERROR: TK_FULLTEST_KEY is required for --gate-sharded" >&2
				exit 2
			fi
			load_gate_shard_platforms
			status=0
			shard_args=("${SSOT_ARGS[@]}")
			if [[ "$GATE_DEPLOY_CLOSEOUT" == "1" ]]; then
				shard_args+=(--deploy-closeout)
			fi
			if [[ -n "$SKIP_RECENT_FILE" ]]; then
				shard_args+=(--skip-recent-file "$SKIP_RECENT_FILE")
			fi
			for platform in "${GATE_SHARD_PLATFORMS[@]}"; do
				echo "# SSOT display gate shard platform=${platform}"
				if ! python3 "$ROOT/ops/test/gateway_model_ssot_matrix.py" gate \
					--only-platform "$platform" \
					"${shard_args[@]}"; then
					status=1
				fi
				sleep "$GATE_SHARD_SLEEP"
			done
			exit "$status"
		fi
		if [[ "$GATE_DEPLOY_CANARY" == "1" ]]; then
			if [[ -z "${TK_FULLTEST_KEY:-}" ]]; then
				echo "ERROR: TK_FULLTEST_KEY is required for --deploy-canary" >&2
				exit 2
			fi
			canary_cmd=(python3 "$ROOT/ops/test/gateway_model_ssot_matrix.py" gate --deploy-canary)
			if [[ "$GATE_DEPLOY_CLOSEOUT" == "1" ]]; then
				canary_cmd+=(--deploy-closeout)
			fi
			if ((${#SSOT_ARGS[@]} > 0)); then
				canary_cmd+=("${SSOT_ARGS[@]}")
			fi
			if [[ -n "$SKIP_RECENT_FILE" ]]; then
				canary_cmd+=(--skip-recent-file "$SKIP_RECENT_FILE")
			fi
			exec "${canary_cmd[@]}"
		fi
		exec "${ssot_cmd[@]}"
		;;
	all)
		"${direct_cmd[@]}"
		"${universal_cmd[@]}"
		;;
esac
