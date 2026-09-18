---
title: Rolling Capacity Saturation Scheduling
status: approved
approved_by: "user (conversation approval of revised design, 2026-09-17)"
approved_at: "2026-09-17"
authors: [codex]
created_at: "2026-09-17"
related_stories: [US-050]
---

# Rolling Capacity Saturation Scheduling

## Decision

Upgrade the existing saturation counters from a fixed 90-second window to a
rolling 10-minute window. A candidate becomes saturated after 12 capacity
failures in that window. The existing priority and score penalties remain the
only scheduling inputs, but their magnitude now grows with the live failure
count so sustained pressure keeps sinking the candidate behind healthy peers.

Classified account-capacity failures must switch accounts immediately instead
of retrying the same account. Shared model-capacity failures retain their
existing model-scoped retry behavior.

## Invariants

- Healthy accounts are preferred over saturated accounts.
- Among saturated accounts, the lower rolling failure count is preferred.
- A saturated account is deprioritized, never removed from the candidate pool.
- Existing `priority` and `score` remain the only ranking keys.
- Existing saturation counters remain the single live-state owner.
- Capacity failure classification remains platform-specific and conservative.

## Acceptance

- Rolling counters expire individual events after 10 minutes.
- The 12-event threshold and continuous penalty apply across existing
  saturation counters.
- Classified capacity failures no longer retry the same account.
- Redis failures remain best-effort and do not break selection.
