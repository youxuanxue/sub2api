#!/usr/bin/env bash
# Open a PR when daily cc TLS capture drifts from tk_canonical_cc_oauth baseline.
# All branch / commit / push operations run inside an isolated git worktree so
# the user's main checkout (and current branch) is never silently switched.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PY="$SCRIPT_DIR/capture_cc_fingerprint.py"

bundle="${1:-}"
if [[ -z "$bundle" || ! -f "$bundle" ]]; then
  echo "usage: $0 <path-to-cc-capture.bundle.json>" >&2
  exit 1
fi

cd "$REPO_ROOT"

if python3 "$PY" check-tls --bundle "$bundle" >/dev/null 2>&1; then
  echo "cc_fingerprint_open_tls_drift_pr: no TLS mismatch in bundle; skip PR" >&2
  exit 0
fi

if ! command -v git >/dev/null 2>&1; then
  echo "error: git required to open drift PR" >&2
  exit 1
fi

stamp="$(date -u +%Y%m%d)"
branch="feature/cc-tls-drift-${stamp}"
report="$(python3 "$PY" diff --bundle "$bundle")"

git fetch origin main 2>/dev/null || git fetch origin master 2>/dev/null || true  # preflight-allow: swallow
base="main"
if ! git show-ref --verify --quiet refs/remotes/origin/main; then
  base="master"
fi

WT_DIR="$REPO_ROOT/.tls_list/.drift-worktree-${stamp}-$$"
_cleanup_worktree() {
  if [[ -d "$WT_DIR" ]]; then
    git worktree remove --force "$WT_DIR" 2>/dev/null || rm -rf "$WT_DIR"  # preflight-allow: swallow
  fi
}
trap _cleanup_worktree EXIT

if git show-ref --verify --quiet "refs/heads/${branch}"; then
  git worktree add "$WT_DIR" "${branch}"
else
  git worktree add -B "${branch}" "$WT_DIR" "origin/${base}"
fi

spec_path="$(python3 "$PY" write-drift-spec --bundle "$bundle" \
  --out "$WT_DIR/docs/spec-delta-cc-tls-drift-${stamp}.md")"
spec_rel_path="${spec_path#"$WT_DIR/"}"

(
  cd "$WT_DIR"
  git add "$spec_rel_path"
  if git diff --cached --quiet; then
    echo "error: nothing to commit for drift PR" >&2
    exit 1
  fi
  git commit -m "$(cat <<EOF
docs: cc TLS drift evidence from daily capture (${stamp})

Automated sessionStart hook detected ja3 drift vs tk_canonical_cc_oauth.
Follow tokenkey-cc-fingerprint-alignment skill to update profile + constants.

EOF
)"
)

if ! command -v gh >/dev/null 2>&1; then
  echo "WARN: gh not installed; branch ${branch} committed in worktree only" >&2
  echo "$report"
  exit 0
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "WARN: gh not authenticated; branch ${branch} committed in worktree only" >&2
  echo "$report"
  exit 0
fi

pr_url="$(
  cd "$WT_DIR"
  git push -u origin "${branch}"
  gh pr create \
    --base "${base}" \
    --head "${branch}" \
    --title "fix(cc): align TLS profile with cc capture ${stamp}" \
    --body "$(cat <<EOF
## Summary
- Daily cc TLS capture detected **ja3 drift** vs \`tk_canonical_cc_oauth\`.
- Adds \`${spec_rel_path}\` with diff evidence; human/agent follow-up to update profile + HTTP constants.

## Risk
Regular risk — TLS/HTTP fingerprint alignment (\`tokenkey-cc-fingerprint-alignment\`).

## Validation
- [ ] \`bash ops/anthropic/capture-cc-fingerprint.sh capture\` → \`check-tls\` green
- [ ] Update \`deploy/aws/stage0/tk_canonical_cc_oauth.json\` + \`manage-anthropic-config.py plan/apply/verify\`
- [ ] \`./scripts/preflight.sh\`

\`\`\`text
${report}
\`\`\`
EOF
)"
)"

echo "opened PR: ${pr_url}"
