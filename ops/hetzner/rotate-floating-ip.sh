#!/usr/bin/env bash
# Phase-1 stub: Floating IP rotation for Hetzner edges.
# Full implementation lands with Phase-2 canary (exclusion registry + DNS).
# Anchor: docs/approved/hetzner-cloud-full-migration.md
set -euo pipefail

echo "ops/hetzner/rotate-floating-ip.sh: not implemented in Phase-1 (refuse)" >&2
echo "Use Lightsail rotate path until Hetzner edge is deployable=true." >&2
exit 1
