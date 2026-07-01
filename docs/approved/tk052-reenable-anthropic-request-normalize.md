---
title: tk_052 Re-enable Anthropic Request Normalize
status: approved
approved_by: xuejiao
approved_at: 2026-07-01
authors: [agent]
created: 2026-07-01
related_prs: [1111]
related_commits: []
---

# tk_052 Re-enable Anthropic Request Normalize

## Scope

Migration `backend/migrations/tk_052_reenable_anthropic_request_normalize.sql` only:

- `UPDATE settings` to flip `tk_anthropic_request_normalize_enabled` from `false` → `true` where an operator had disabled it.
- `INSERT ... ON CONFLICT DO NOTHING` when the key is absent.

No table DDL. Reversible by setting the same settings key back to `false`.

## Rationale

Code default and `tk_010` migration expect normalize **enabled**. A prod operator `false` silently disabled tool_choice / thinking / CC prompt-surface fixes. Live prod was corrected via SSM; migration aligns fleet DBs on deploy.
