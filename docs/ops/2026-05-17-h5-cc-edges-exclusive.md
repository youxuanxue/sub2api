# H5 cc-edges → Exclusive + Admin Whitelist — 2026-05-17

Operational record of the cc-edges visibility hardening applied on
2026-05-17 via SSM → `tokenkey-prod-stage0`.

## Decision

Before:

- `cc-edges` (id=15) `is_exclusive=false` → **public group**, visible
  in every user's "select a group" dropdown.
- `user_allowed_groups` table was **empty** for the entire prod stack.
- The "admin-only" intent recorded in H4 was operational convention,
  not enforced — any user could mint an API key against cc-edges
  through the standard create flow.

After:

- `cc-edges` `is_exclusive=true` → **private group**.
- `user_allowed_groups` has one row: `(user_id=1, group_id=15)`,
  binding admin (`admin@tokenkey.dev`) to cc-edges.
- The `CanBindGroup` check (backend/internal/service/user.go:81) now
  hides cc-edges from every non-admin user at API-key create/update
  time.

## Why this shape

H4 (earlier 2026-05-17) noted in its follow-up that
`user_allowed_groups` had no row for cc-edges and admin visibility
depended on "whoever provisioned the keys having had elevated rights
at provisioning time, not a persisted ACL". H5 closes that gap.

The schema already has the right pieces — `groups.is_exclusive` +
`user_allowed_groups (user_id, group_id)` — they were just unused.
H5 turns them on; no schema change, no code change.

**Runtime impact: zero.** The gateway / ratelimit path does **not**
call `CanBindGroup`; only `api_key_service.Create` /
`api_key_service.Update` do. The 3 admin API keys already on cc-edges
(id=77/81/83, user_id=1) continue to work unchanged.

## Apply

Single SSM transaction on `tokenkey-prod-stage0` (us-east-1):

```sql
BEGIN;

-- H5.a — flip cc-edges to exclusive
UPDATE groups
SET is_exclusive = true,
    updated_at   = NOW()
WHERE id           = 15
  AND name         = 'cc-edges'
  AND is_exclusive = false;

-- H5.b — admin allowed for cc-edges
INSERT INTO user_allowed_groups (user_id, group_id, created_at)
VALUES (1, 15, NOW())
ON CONFLICT (user_id, group_id) DO NOTHING;

-- Verify
SELECT 'H5.a groups' AS step, id, name, is_exclusive, status
FROM groups WHERE id = 15;

SELECT 'H5.b user_allowed_groups (cc-edges)' AS step,
       uag.user_id, u.email, uag.group_id, g.name AS group_name
FROM user_allowed_groups uag
JOIN users  u ON u.id = uag.user_id  AND u.deleted_at IS NULL
JOIN groups g ON g.id = uag.group_id AND g.deleted_at IS NULL
WHERE uag.group_id = 15
ORDER BY uag.user_id;

COMMIT;
```

Both updates are idempotent: H5.a is guarded by `is_exclusive=false`
(no-op if already exclusive), H5.b uses `ON CONFLICT (user_id, group_id)
DO NOTHING`.

### SSM audit ID

- prod apply: `2985e164-5a07-46ba-bf7d-1981d27d016a` (us-east-1)
- `Status=Success`, `ResponseCode=0`
- H5.a verify: `cc-edges, is_exclusive=t, status=active`
- H5.b verify: `user_id=1, admin@tokenkey.dev, group_id=15, cc-edges`

## Visibility model recap

```
┌─────────────────────────────────────────────────────────┐
│  groups.is_exclusive = true                             │
│       │                                                 │
│       ▼                                                 │
│  Frontend / GetAvailableGroups filters cc-edges OUT     │
│  for every user whose user_allowed_groups does not list │
│  (user_id, group_id=15).                                │
│       │                                                 │
│       ▼                                                 │
│  api_key create/update enforces CanBindGroup            │
│  (backend/internal/service/api_key_service.go:325/557)  │
│  → returns ErrGroupNotAllowed (HTTP 403) for non-admin. │
│                                                         │
│  Gateway request-time path: NOT re-checked.             │
│  Existing keys keep working until revoked.              │
└─────────────────────────────────────────────────────────┘
```

## Rollback

```sql
BEGIN;
DELETE FROM user_allowed_groups WHERE user_id=1 AND group_id=15;
UPDATE groups SET is_exclusive=false, updated_at=NOW()
  WHERE id=15 AND name='cc-edges' AND is_exclusive=true;
COMMIT;
```

This restores the public-group state. Existing admin API keys on
cc-edges are unaffected by rollback (they survived the change in the
first place because gateway does not re-check).

## Follow-ups

- **Add more allowed users on demand**: future grants are
  `INSERT INTO user_allowed_groups (user_id, group_id, created_at)
  VALUES (<uid>, 15, NOW()) ON CONFLICT DO NOTHING;`. No SKILL update
  needed — the same pattern applies.
- **`cc-uk1-test` / `cc-fra1-tes` admin debug keys** (id=81/83) still
  target retired edge OAuth accounts (H3). If admins no longer need
  them, delete in a future cleanup PR.
- **First exclusive group on prod**: cc-edges is the only
  `is_exclusive=true` row at time of writing. Future internal/admin
  groups should follow the same pattern (exclusive + targeted
  `user_allowed_groups` rows) rather than relying on the absence of
  user awareness.
