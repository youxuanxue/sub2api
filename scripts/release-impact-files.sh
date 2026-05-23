#!/usr/bin/env bash
# release-impact-files.sh — Mechanical file-change classifier for the
# tokenkey-stage0-release-rollout skill §"完成后：+5min/+10min/+15min Step A".
#
# The skill's intent was: after `git diff --name-only PREV NEW`, group changed
# files into buckets so the model can decide which observable hooks to grep
# for in post-release logs. Bucket boundaries are mechanical; the per-file
# semantic ("which hook name should I watch") stays as prompt judgment.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Bucket membership is decided by stable path globs/patterns (see below).
#   - Output is a single JSON object on stdout; same input → same output bytes.
#   - File paths are sorted lexicographically inside each bucket.
#
# Usage:
#   bash scripts/release-impact-files.sh <BASE_REF> <HEAD_REF>
#
# Buckets (key in output JSON):
#   backend_handler        — backend/internal/handler/**
#   backend_service        — backend/internal/service/**
#   backend_repository     — backend/internal/repository/**
#   backend_middleware     — backend/internal/middleware/**
#   backend_integration    — backend/internal/integration/**
#   backend_config         — backend/internal/config/**, backend/cmd/server/*.go
#   backend_relay          — backend/internal/relay/**, backend/internal/pkg/**
#   backend_schema         — backend/ent/schema/**, backend/migrations/**
#   backend_wire_gen       — backend/cmd/server/wire_gen.go, backend/ent/**.go (generated)
#   frontend_views         — frontend/src/views/**, frontend/src/components/**
#   frontend_api           — frontend/src/api/**
#   frontend_stores        — frontend/src/stores/**, frontend/src/composables/**
#   frontend_other         — frontend/src/** (catch-all under src/)
#   ci_workflows           — .github/workflows/**
#   deploy_stage0          — deploy/aws/** , Dockerfile, scripts/release-tag.sh
#   sentinels              — scripts/sentinels/**
#   docs                   — docs/**, README*.md, *.md (root)
#   other                  — everything else
#
# Files marked as deleted (D status) are listed in a separate "deleted" array
# (alongside their bucket classification) so callers can show §5.x risk.
#
# Exit codes:
#   0 — JSON written
#   1 — usage error / ref unresolvable
#   2 — git failure
set -euo pipefail

if [ "$#" -lt 2 ]; then
  sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
fi

BASE="$1"
HEAD_REF="$2"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

git rev-parse "$BASE" >/dev/null 2>&1 || { echo "[release-impact-files] ERROR: cannot resolve BASE=$BASE" >&2; exit 1; }
git rev-parse "$HEAD_REF" >/dev/null 2>&1 || { echo "[release-impact-files] ERROR: cannot resolve HEAD=$HEAD_REF" >&2; exit 1; }

# name-status: M / A / D / R
DIFF=$(git diff --name-status "$BASE..$HEAD_REF" 2>/dev/null) || {
  echo "[release-impact-files] ERROR: git diff failed" >&2; exit 2; }

export RIF_DIFF_TEXT="$DIFF"
python3 - "$BASE" "$HEAD_REF" <<'PY'
import json
import os
import sys

base, head_ref = sys.argv[1], sys.argv[2]

buckets = {
    "backend_handler":     [],
    "backend_service":     [],
    "backend_repository":  [],
    "backend_middleware":  [],
    "backend_integration": [],
    "backend_config":      [],
    "backend_relay":       [],
    "backend_schema":      [],
    "backend_wire_gen":    [],
    "frontend_views":      [],
    "frontend_api":        [],
    "frontend_stores":     [],
    "frontend_other":      [],
    "ci_workflows":        [],
    "deploy_stage0":       [],
    "sentinels":           [],
    "docs":                [],
    "other":               [],
}

deleted = []
all_changes = []

def classify(path: str) -> str:
    if path.startswith("scripts/sentinels/"):
        return "sentinels"
    if path.startswith(".github/workflows/"):
        return "ci_workflows"
    if path.startswith("backend/ent/schema/") or path.startswith("backend/migrations/"):
        return "backend_schema"
    if path == "backend/cmd/server/wire_gen.go" or (
        path.startswith("backend/ent/") and path.endswith(".go")
    ):
        return "backend_wire_gen"
    if path.startswith("backend/internal/handler/"):
        return "backend_handler"
    if path.startswith("backend/internal/service/"):
        return "backend_service"
    if path.startswith("backend/internal/repository/"):
        return "backend_repository"
    if path.startswith("backend/internal/middleware/"):
        return "backend_middleware"
    if path.startswith("backend/internal/integration/"):
        return "backend_integration"
    if path.startswith("backend/internal/relay/") or path.startswith("backend/internal/pkg/"):
        return "backend_relay"
    if path.startswith("backend/internal/config/") or (
        path.startswith("backend/cmd/server/") and path.endswith(".go") and path != "backend/cmd/server/wire_gen.go"
    ):
        return "backend_config"
    if path.startswith("frontend/src/views/") or path.startswith("frontend/src/components/"):
        return "frontend_views"
    if path.startswith("frontend/src/api/"):
        return "frontend_api"
    if path.startswith("frontend/src/stores/") or path.startswith("frontend/src/composables/"):
        return "frontend_stores"
    if path.startswith("frontend/src/"):
        return "frontend_other"
    if path.startswith("deploy/aws/") or path == "Dockerfile" or path == "scripts/release-tag.sh":
        return "deploy_stage0"
    if path.startswith("docs/") or path.endswith("README.md") or (
        "/" not in path and path.endswith(".md")
    ):
        return "docs"
    return "other"

for line in os.environ.get("RIF_DIFF_TEXT", "").splitlines():
    if not line:
        continue
    parts = line.split("\t")
    if len(parts) < 2:
        continue
    status = parts[0]
    # R100 oldname newname → use newname for classification
    path = parts[-1]
    bucket = classify(path)
    buckets[bucket].append(path)
    all_changes.append({"path": path, "status": status[0], "bucket": bucket})
    if status.startswith("D"):
        deleted.append({"path": path, "bucket": bucket})

for k in buckets:
    buckets[k].sort()
all_changes.sort(key=lambda r: r["path"])
deleted.sort(key=lambda r: r["path"])

output = {
    "range": {"base": base, "head": head_ref},
    "totals": {
        "files_changed": len(all_changes),
        "files_deleted": len(deleted),
        "buckets_touched": sum(1 for v in buckets.values() if v),
    },
    "buckets": buckets,
    "deleted": deleted,
}
json.dump(output, sys.stdout, indent=2, sort_keys=True)
sys.stdout.write("\n")
PY
