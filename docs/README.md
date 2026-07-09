# TokenKey docs

This directory is intentionally split by audience and source of truth. Do not
add new root-level design notes unless no existing bucket fits.

## Current entry points

| Need | Go to | Rule |
| --- | --- | --- |
| Operator / support action | [`operator/README.md`](operator/README.md) | Human-facing "how to do it" runbooks. |
| Engineering ops / CI baseline / incident facts | [`ops/README.md`](ops/README.md) | Machine-bound baselines, troubleshooting records, infra snapshots, backlog. |
| High-risk approved design | [`approved/README.md`](approved/README.md) | Approval baseline. Keep stable paths; mark status instead of moving casually. |
| Small implementation deltas | [`spec-delta/README.md`](spec-delta/README.md) | Intent records for non-approval changes. |
| Raw evidence and captured upstream pricing | [`evidence/README.md`](evidence/README.md) | Evidence only; not an operator runbook. |
| Account/fingerprint references | [`accounts/README.md`](accounts/README.md) | Account onboarding and upstream-account baselines. |
| Historical material | [`archive/README.md`](archive/README.md) | Superseded designs, mocks, and old proposals. |
| Public in-product pages | [`public/`](public/) | Only this subtree may be synced to production Pages. |
| Agent/process reference | [`global/`](global/) | Overflow for root `CLAUDE.md` and upstream-merge discipline. |

## Boundaries

- Root `README*.md` and generic deploy files are upstream-owned. Keep TokenKey
  additions in TokenKey docs and link out instead of duplicating steps there.
- `docs/approved/*.md` is gated by preflight frontmatter invariants. Use
  `status: archived` for superseded decisions; do not move load-bearing files
  until code, sentinel, and migration references are updated.
- Point-in-time vendor captures belong under `docs/evidence/`, not under
  account/operator docs.
- Public customer docs live under `docs/public/` only. Internal docs must never
  be synced to Pages.
