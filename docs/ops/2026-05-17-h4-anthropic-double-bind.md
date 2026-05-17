# H4 Anthropic Pool Double-Bind — 2026-05-17

Operational record of two coupled changes:

1. **Data**: prod `cc-us1-oauth` (id=42) added back to `cc-edges`
   (id=15) while keeping its `default` (id=1) binding from H3
   (2026-05-17 earlier today). Net effect: the stub now sits in both
   groups, default carries user traffic, cc-edges carries admin debug
   traffic, both pointing at the same upstream edge.
2. **Skill**: `tokenkey-edge-anthropic-oauth-config` renamed to
   `tokenkey-anthropic-oauth-config` and a new section was added
   declaring the prod stub double-bind rule.

## Why this shape

H3 left `cc-edges` empty by moving the only stub (`cc-us1-oauth`) to
`default`. That correctly redirected user traffic, but inspecting
`api_keys` revealed `cc-edges` still hosted 3 admin debug keys
(`edge-MAIN_GATEWAY_EDGE_SMOKE_API_KEY`, `cc-uk1-test`, `cc-fra1-tes`)
that would have lost a schedulable account. A 4th key (`zw`, user_id=9,
non-admin) was identified on `cc-edges` and migrated by hand to
`GPT专线` (group_id=2) before this change, restoring the admin-only
invariant.

The fix is to **double-bind** the active stub: same upstream, two
group entries, two different audiences. The API-key→group association
in `api_keys.group_id` (single-valued) decides which audience the
caller is in; `user_allowed_groups` controls which groups a user can
even pick from when minting a key. Admin (`user_id=1`) keeps both
`default` and `cc-edges` visibility; normal users only see `default`.

This pattern generalizes to any future anthropic forward stub: ship it
with both bindings from day one, so the admin debug path is never a
separate provisioning afterthought.

## Apply

Single SSM transaction on `tokenkey-prod-stage0` (us-east-1):

```sql
BEGIN;
INSERT INTO account_groups (account_id, group_id, priority, created_at)
VALUES (42, 15, 1, NOW())
ON CONFLICT (account_id, group_id) DO NOTHING;

SELECT 'H4 verify' AS step, ag.account_id, a.name AS account_name,
       g.id AS group_id, g.name AS group_name, ag.priority
FROM account_groups ag
JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL
JOIN groups   g ON g.id = ag.group_id  AND g.deleted_at IS NULL
WHERE ag.account_id = 42
ORDER BY g.id;

SELECT 'cc-edges bindings count' AS step,
       count(*) AS n FROM account_groups WHERE group_id = 15;
COMMIT;
```

`ON CONFLICT (account_id, group_id) DO NOTHING` keeps the apply
idempotent. The verify select inside the transaction confirms the
two bindings; the count select confirms `cc-edges` is no longer empty.

### SSM audit ID

- prod apply: `b2145d60-ce8a-4f8e-8870-f40b39a1d814` (us-east-1)
- `Status=Success`, `ResponseCode=0`
- post-apply verify rows: `(42, cc-us1-oauth, 1, default, prio=1)` +
  `(42, cc-us1-oauth, 15, cc-edges, prio=1)`
- `cc-edges` bindings count = 1

## Visibility model

| concern | mechanism | enforced at |
| ---- | ---- | ---- |
| Which group does a request use? | `api_keys.group_id` (single value) | per API key |
| Which groups can a user mint keys for? | `user_allowed_groups (user_id, group_id)` | per user |
| Where does each group's anthropic request go upstream? | `account_groups (account_id, group_id)` | per (account, group) pair |

`cc-edges` is admin-only **operationally**, not via a schema flag:
admins (`user_id=1`) get a `user_allowed_groups` row pointing at
`cc-edges`, normal users do not. Any future deviation (e.g. another
non-admin user appearing on `cc-edges`) should be caught by audit
queries, not by relying on the DB schema.

## Pre-apply audit (cc-edges state before H4)

```
api_keys on group_id=15 (cc-edges):
  id=77  user_id=1  edge-MAIN_GATEWAY_EDGE_SMOKE_API_KEY   (admin debug)
  id=81  user_id=1  cc-uk1-test                            (admin debug)
  id=83  user_id=1  cc-fra1-tes                            (admin debug)
```

(`zw`, id=85, user_id=9 was migrated to `GPT专线` (id=2) before this
change; reverified to confirm.)

## Skill change

`.cursor/skills/tokenkey-edge-anthropic-oauth-config/` →
`.cursor/skills/tokenkey-anthropic-oauth-config/`. The skill now
covers both edge OAuth tier management (the original scope) and the
prod control-plane stub binding rule.

The new section "prod 控制面：anthropic stub 双绑规则" near the top of
the skill states:

- Every prod anthropic forward stub must bind both `default` (id=1)
  and `cc-edges` (id=15) at create time.
- Bindings are added/removed in pairs — single-binding states are
  considered drift.
- Edge databases do **not** mirror `cc-edges`; admin debug is a prod
  control-plane concept.

## Rollback

Drop the `cc-edges` binding only (H3 end state):

```sql
BEGIN;
DELETE FROM account_groups WHERE account_id = 42 AND group_id = 15;
COMMIT;
```

The skill rename is reversible with `git mv` in the opposite
direction, but doing so would invalidate any in-flight slash-command
references using the new name.

## Follow-ups

- The `cc-uk1-test` and `cc-fra1-tes` admin debug keys still exist
  even though their target edges were retired in H3. If admins still
  need them as historical-test artifacts, no action; otherwise they
  should be deleted in a future cleanup PR.
- Confirm `user_allowed_groups` actually has rows for `user_id=1` on
  `cc-edges` (id=15). The pre-apply query returned no rows for that
  group — if the table is genuinely empty for cc-edges, the admin
  visibility today depends entirely on whoever provisioned the keys
  having had elevated rights at provisioning time, not on a persisted
  ACL. Worth one targeted hardening PR.
