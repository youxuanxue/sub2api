---
title: Upstream merge 2026-07-10 — schema migrations approval
status: approved
approved_by: "upstream-merge-agent (automated, headless CI)"
approved_at: "2026-07-10"
authors: [tk-upstream-agent[bot]]
related_commits: [c093a20bd]
---

# Upstream merge 2026-07-10 — migration approval

Approves the four upstream-originating migrations introduced in this merge:

| File | Purpose |
|------|---------|
| `backend/migrations/170_add_grok_video_pricing_controls.sql` | Grok video pricing control columns |
| `backend/migrations/171_allow_video_usage_without_image_size.sql` | Relax constraint to allow video usage rows without image_size |
| `backend/migrations/172_video_per_second_billing_metadata.sql` | Per-second billing metadata for video |
| `backend/migrations/173_allow_cyber_blocked_usage_request_type.sql` | Allow cyber_blocked as a valid request_type |

These migrations originate from `Wei-Shaw/sub2api` upstream and are merged verbatim.
They extend the schema in a backward-compatible, additive manner (new columns/constraints only).
No TK-custom data is modified.

## Risk assessment

**Low** — all changes are additive (new nullable columns or constraint relaxations).
No existing column is removed, renamed, or type-changed.
Rollback: drop the added columns/constraints (no data loss).
