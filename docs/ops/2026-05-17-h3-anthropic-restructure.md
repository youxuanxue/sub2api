# H3 Anthropic Pool Restructure — 2026-05-17

Operational record of the prod + edge anthropic pool restructure applied
on 2026-05-17 via SSM → `tokenkey-postgres`.

## Decision

Before:

```
prod cc-edges (id=15, anthropic) -> [cc-us1-oauth, cc-uk1-oauth, cc-fra1-oauth]  forward to api-{us1,uk1,fra1}.tokenkey.dev
                                                                                     │
                                                                                     ▼
                                                                                  edge uk1: cc-en-ld-ec2-16-1-a (l1 OAuth)
                                                                                  edge fra1: cc-fr-fra-ec2-5-1-a (l1 OAuth)
                                                                                  edge us1:  cc-am-or-ec2-5-1-b  (l2 OAuth)
```

After:

```
prod cc-edges (id=15, anthropic) -> []   (admin-only slot, empty)
prod default  (id=1,  anthropic) -> [cc-us1-oauth]   forward to api-us1.tokenkey.dev
                                                              │
                                                              ▼
                                                          edge us1: cc-am-or-ec2-5-1-b (l2 OAuth)
```

`uk1` and `fra1` OAuth accounts are retired permanently (refresh tokens
expected to lapse from inactivity; project decision is to not maintain
them). Stub `cc-uk1-oauth` / `cc-fra1-oauth` on prod are soft-deleted in
the same change so the prod side cannot route to dead upstreams.

## Why this shape

1. **`cc-edges` becomes admin-only.** Anthropic user traffic now flows
   through the `default` (id=1) group, which already exists as the
   natural fallback. The `cc-edges` group row is retained (not deleted)
   so future admin-API-key bindings can land there without re-creating
   the group.
2. **Single anthropic upstream (us1).** With uk1/fra1 retired and
   `cc-us1-oauth.schedulable=false` set manually overnight, the system
   reflects the operator's current intent — us1 is the sole anthropic
   path and is paused at the operator's discretion. The schedulable
   flag is not modified by this change.
3. **Soft-delete, not hard-delete.** Setting `deleted_at`, `schedulable
   =false`, and `status='disabled'` together makes the row inert in
   every code path that filters on any of these fields, while keeping
   the audit trail (created_at, last_used_at, credentials key inventory)
   intact for forensic review.

## Apply

Single transaction per target (prod, edge uk1, edge fra1). SQL was
hand-written for prod and edges, then run sequentially via SSM
`AWS-RunShellScript` → `sudo docker exec -i tokenkey-postgres psql`.

### Prod transaction (`tokenkey-prod-stage0`, us-east-1)

```sql
BEGIN;

-- H3.a — rebind cc-us1-oauth (id=42): cc-edges (15) -> default (1)
DELETE FROM account_groups WHERE account_id = 42 AND group_id = 15;
INSERT INTO account_groups (account_id, group_id, priority, created_at)
VALUES (42, 1, 1, NOW());

-- H3.b — drop bindings + soft-delete deprecated stubs
DELETE FROM account_groups WHERE account_id IN (40, 41);

UPDATE accounts
SET deleted_at = NOW(),
    schedulable = false,
    status      = 'disabled',
    updated_at  = NOW()
WHERE id IN (40, 41)
  AND name IN ('cc-uk1-oauth', 'cc-fra1-oauth')
  AND platform = 'anthropic'
  AND deleted_at IS NULL;

COMMIT;
```

### Edge uk1 transaction (`tokenkey-edge-uk1-stage0`, eu-west-2)

```sql
BEGIN;
DELETE FROM account_groups WHERE account_id = 2;
UPDATE accounts
SET deleted_at = NOW(),
    schedulable = false,
    status      = 'disabled',
    updated_at  = NOW()
WHERE id = 2
  AND name = 'cc-en-ld-ec2-16-1-a'
  AND platform = 'anthropic'
  AND type = 'oauth'
  AND deleted_at IS NULL;
COMMIT;
```

### Edge fra1 transaction (`tokenkey-edge-fra1-stage0`, eu-west-3)

```sql
BEGIN;
DELETE FROM account_groups WHERE account_id = 1;
UPDATE accounts
SET deleted_at = NOW(),
    schedulable = false,
    status      = 'disabled',
    updated_at  = NOW()
WHERE id = 1
  AND name = 'cc-fr-fra-ec2-5-1-a'
  AND platform = 'anthropic'
  AND type = 'oauth'
  AND deleted_at IS NULL;
COMMIT;
```

### SSM audit IDs

- prod: `0ddbe309-2d97-4ce1-bd3f-40778141f542` (us-east-1)
- edge uk1: `81069236-96c5-474f-9932-59b04e7aa349` (eu-west-2)
- edge fra1: `5284d828-0a0c-450d-9a75-9538f41961ba` (eu-west-3)

All three returned `Status=Success`, `ResponseCode=0`. Post-apply
verification queries (embedded in the prod SQL) confirmed:

- `cc-uk1-oauth` (40): `schedulable=false`, `status=disabled`, `deleted=true`
- `cc-fra1-oauth` (41): `schedulable=false`, `status=disabled`, `deleted=true`
- `cc-us1-oauth` (42): `schedulable=false` (preserved), `status=active`, `deleted=false`
- bindings: `[(42, default, priority=1)]`
- `cc-edges` (15) binding count = 0
- edge uk1 `cc-en-ld-ec2-16-1-a` (2): soft-deleted, no bindings
- edge fra1 `cc-fr-fra-ec2-5-1-a` (1): soft-deleted, no bindings

## Rollback (if ever needed)

The OAuth credentials on the retired edges have expected to lapse
(refresh_token inactivity); rollback is **not safe to assume** without
reissuing OAuth. The supported recovery path is forward-only: spin up a
fresh OAuth account, register it through the
`tokenkey-edge-anthropic-oauth-config` skill, and re-introduce the edge
to traffic via a new stub. If a true rollback of this specific change
is needed (e.g., investigation requires the soft-deleted rows to be
visible to live queries), reverse the soft-delete columns and bindings:

```sql
-- Prod
BEGIN;
UPDATE accounts SET deleted_at = NULL, status = 'active', updated_at = NOW()
  WHERE id IN (40, 41);
DELETE FROM account_groups WHERE account_id = 42 AND group_id = 1;
INSERT INTO account_groups (account_id, group_id, priority, created_at) VALUES
  (40, 15, 1, NOW()), (41, 15, 1, NOW()), (42, 15, 1, NOW());
COMMIT;

-- Edge uk1
BEGIN;
UPDATE accounts SET deleted_at = NULL, status = 'active', updated_at = NOW() WHERE id = 2;
INSERT INTO account_groups (account_id, group_id, priority, created_at) VALUES (2, 1, 1, NOW());
COMMIT;

-- Edge fra1
BEGIN;
UPDATE accounts SET deleted_at = NULL, status = 'active', updated_at = NOW() WHERE id = 1;
INSERT INTO account_groups (account_id, group_id, priority, created_at) VALUES (1, 1, 1, NOW());
COMMIT;
```

Note that resurrecting the row does not resurrect a working OAuth token.

## Follow-ups

- **S2 alignment guard** — separate PR. When tier or `extra.base_rpm`
  on an account changes, the binding group's `rpm_limit` must satisfy
  `rpm_limit ≥ base_rpm` (otherwise the group becomes the bottleneck).
  This rule was tripped on edge uk1/fra1 where `default.rpm_limit=3`
  was less than `base_rpm=6`. With uk1/fra1 now retired the immediate
  hazard is gone, but the rule should be encoded as a script before
  any future tier/RPM change is approved.
- **Edge uk1 / fra1 infrastructure** — the EC2 stacks
  `tokenkey-edge-uk1-stage0` / `tokenkey-edge-fra1-stage0` and their
  DNS (`api-uk1.tokenkey.dev`, `api-fra1.tokenkey.dev`) are still up.
  Retiring the OAuth row does not bring down the host. If the project
  has decided not to use these regions at all, a separate teardown PR
  should mark the targets in `deploy/aws/stage0/edge-targets.json` as
  `deployable: false` (or remove them) and tear down the CloudFormation
  stacks. Out of scope here.
- **us1 admin@ account** — `admin@api-us1.tokenkey.dev` on edge us1
  remains untouched per operator decision. Not modified by this change.
