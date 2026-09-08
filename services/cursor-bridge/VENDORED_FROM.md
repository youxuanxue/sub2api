# Cursor bridge provenance

Source: https://github.com/Sunnyender-org/new-api/tree/9ab86e3bc0ca368df585edcc096ec172263d222d/cursor_agent_sidecar

Commit: `9ab86e3bc0ca368df585edcc096ec172263d222d` (new-api PR #6869).
The unmodified source passed all 52 tests locally on 2026-09-07 using its locked
SDK 1.0.27. The bridge preserves the source modules and tests under `upstream/`.
Repository license: AGPL-3.0, reproduced in `upstream/LICENSE`. The official
Cursor SDK has separate terms, reproduced in `upstream/CURSOR-SDK-LICENSE.md`.

TokenKey patches:

- Export the existing HTTP server and install an internal request guard before
  routing. `server.tk.mjs` is the production entrypoint; upstream's direct entry
  remains available solely for upstream regression tests.
- Authenticate the internal transport separately from Cursor credentials, bind
  authorization sessions to the administrator, and deliver credentials only to
  the authenticated TokenKey backend through a single-claim exchange.
- Preserve explicit model parameters, bound runs, normalize per-turn usage and
  reject replayed billed responses rather than billing them twice.
- Pin the SDK to 1.0.31 after real subscription authorization and inference;
  retain both dependency locks and test the original 52 upstream cases.
- Use an explicit temporary SDK store for every run, disable environment-key
  fallback and restart recovery, and bound cancellation to five seconds.
- Supply a TokenKey parked-turn estimate when the SDK reports no new usage;
  cumulative deltas deduct prior emissions and retain non-negative bucket floors.
- Remove credential-bearing proxy URLs from diagnostic output.
- Release completed tool sessions immediately when replay is rejected, so replay
  retention cannot consume active capacity or delay draining.

Upgrade the source and SDK independently; rerun both upstream and TokenKey tests
and real-account inference/tool checks before changing the deployment pin.
