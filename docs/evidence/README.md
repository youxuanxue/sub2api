# Evidence archive

This tree stores point-in-time evidence used by code comments, pricing overlays,
incident reports, and decision records. It is not an operator entry point.

Use evidence files for provenance, then link the current runbook or SSOT from
`docs/README.md`, `docs/operator/README.md`, or `ops/*/README.md`.

## Buckets

| Directory | Contents |
| --- | --- |
| [`pricing/`](pricing/) | Captured vendor pricing pages and pricing derivation evidence. |

Fingerprint provenance: [`fingerprint/antigravity-spawn-20260629.json`](fingerprint/antigravity-spawn-20260629.json) backs the spawn-validation decision in the Antigravity changelog. Generated release-watch state and unreferenced version snapshots stay in ignored `.cache/`.
