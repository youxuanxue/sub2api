---
title: Disable Cancel-Storm Detector
status: approved
approved_by: "xuejiao (operator directive, PR #957)"
approved_at: 2026-06-23
authors: [agent]
created: 2026-06-23
related_prs: [957]
related_commits: []
---

# Disable Cancel-Storm Detector

## Intent

Retire the per-API-key cancel-storm detector after prod generated noisy
`detect_only` alerts for short-window client cancels. The intended post-change
state is no cancel-storm alerting and no cancel-rate window accounting on the
gateway request hot path.

## Scope

- Remove the `OpsErrorLoggerMiddleware` cancel-storm observation hook.
- Remove the in-process detector, its Feishu alert path, and its unit tests.
- Remove the gateway sentinel that required the hook to stay present.
- Add a follow-up migration that forces any existing `cancel_storm_config` row
  back to `mode: off`.

## Non-Goals

- Do not change the client-cancel attribution classifier that keeps
  `context canceled` traffic out of provider-owned upstream error rate.
- Do not change public API, Web UI, billing, credentials, or schema shape.

## Rollback

Revert the runtime-removal commit if cancel-storm detection is needed again.
The disabling migration is intentionally conservative: it only writes the
existing settings row back to `off` and does not drop data or schema.
