#!/usr/bin/env bash
# Single source of truth for the Stage0 deploy TAG-format gate.
#
# Shared by all three deploy workflows so the tag-format rule never grows
# divergent copies:
#   - .github/workflows/deploy-stage0.yml            (prod)
#   - .github/workflows/deploy-edge-lightsail-stage0.yml (Lightsail edge)
#
# History: the regex was inlined in all three; the Lightsail copy had already
# dropped the "(optionally -rc.N / -beta.N)" note from its error message — the
# exact "patch one, the others drift" failure this script prevents. Re-inlining
# the regex is blocked by the `Stage0 deploy tag-validation sharing` preflight
# sentinel (scripts/preflight.sh).
#
# Scope: FORMAT only. The "tag is required for operation=X" empty-check stays in
# the edge workflows (its wording is operation-contextual, which this script has
# no business knowing). Prod always requires a tag, so it calls this directly.
#
# Usage: validate-deploy-tag.sh <tag>   (falls back to $INPUT_TAG)
# Exit 0 on a well-formed tag; exit 1 (with a ::error:: annotation) otherwise.
set -euo pipefail

TAG="${1:-${INPUT_TAG:-}}"

if [ -z "$TAG" ]; then
  echo "::error::validate-deploy-tag: no tag provided (pass as \$1 or set INPUT_TAG)" >&2
  exit 1
fi

# Canonical Stage0 release-tag shape: X.Y.Z, optionally -rc.N or -beta.N.
if ! printf '%s' "$TAG" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$'; then
  echo "::error::tag must match X.Y.Z (optionally -rc.N / -beta.N), got: $TAG" >&2
  exit 1
fi

echo "ok: tag $TAG matches X.Y.Z (optionally -rc.N / -beta.N)"
