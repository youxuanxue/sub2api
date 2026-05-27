#!/usr/bin/env bash
# Open a PR when daily cc TLS capture drifts from tk_canonical_cc_oauth baseline.
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
spec_path="$(python3 "$PY" write-drift-spec --bundle "$bundle")"
report="$(python3 "$PY" diff --bundle "$bundle")"

git fetch origin main 2>/dev/null || git fetch origin master 2>/dev/null || true
base="main"
if ! git show-ref --verify --quiet refs/remotes/origin/main; then
  base="master"
fi

if git show-ref --verify --quiet "refs/heads/${branch}"; then
  git checkout "${branch}"
else
  git checkout -B "${branch}" "origin/${base}" 2>/dev/null || git checkout -B "${branch}" "${base}"
fi

git add "$spec_path"
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

if ! command -v gh >/dev/null 2>&1; then
  echo "WARN: gh not installed; branch ${branch} committed locally only" >&2
  echo "$report"
  exit 0
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "WARN: gh not authenticated; branch ${branch} committed locally only" >&2
  echo "$report"
  exit 0
fi

git push -u origin "${branch}"
pr_url="$(
  gh pr create \
    --base "${base}" \
    --head "${branch}" \
    --title "fix(cc): align TLS profile with cc capture ${stamp}" \
    --body "$(cat <<EOF
## Summary
- Daily cc TLS capture detected **ja3 drift** vs \`tk_canonical_cc_oauth\`.
- Adds \`${spec_path#"$REPO_ROOT"/}\` with diff evidence; human/agent follow-up to update profile + HTTP constants.

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
