---
name: tokenkey-stage0-edge-ip-rotation
description: >-
  Rotate / replace the egress Elastic IP of a TokenKey Stage0 edge
  (uk1/us1/sg1/fra1/…) when the live IP has been risk-blocked ("polluted") by
  an upstream API (Anthropic / OpenAI / Google). Drives the single canonical
  path: a workflow_dispatch of deploy-edge-stage0.yml with operation=rotate_egress_ip,
  which does a CFN-native UpdateStack — no detach, no IMPORT, no drift class.
  Auto-allocates a clean candidate (checked against edge-polluted-ips.json),
  swaps via CFN, verifies SSM Online + outbound IP + Anthropic/OpenAI/Google
  pollution probe from the edge itself, and auto-reverts on a polluted result.
  The only operator step that remains is the DNS A-record update at Porkbun
  (and committing the retired IP into edge-polluted-ips.json).
---

# TokenKey: rotate an edge gateway's egress EIP

**v2 (OPC).** Replaces the v1 manual multi-step nano-probe / CFN-IMPORT runbook.
The deploy workflow now owns rotation end-to-end; this skill is a thin wrapper
that decides _which workflow input to pass_, not a sequence of bash commands.

The previous v1 runbook in
[`docs/deploy/tokenkey-edge-ip-history.md`](../../../docs/deploy/tokenkey-edge-ip-history.md)
is retained as the **historical & recovery reference** — read it only if (a)
you are doing the one-time per-stack migration via
[`deploy/aws/stage0/migrate-edge-eip-to-parameter.sh`](../../../deploy/aws/stage0/migrate-edge-eip-to-parameter.sh)
on a stack that has not yet been converted to EIP-as-parameter, or (b) the
v2 path failed in a way that requires hand-recovery (rare).

## Why this is short now

The earlier multi-step procedure existed because the CFN template treated the
EIP as a stack-managed resource (`AWS::EC2::EIP` + `EIPAssociation` with
`Retain`). Manual EIP swaps then desynced template-vs-live and required
detach + IMPORT to recover; that recovery sequence (specifically the IMPORT
step on the EIPAssociation) is what silently disconnected the SSM agent on
edge-uk1 on 2026-05-22.

The template has since been refactored so the EIP is an external
`EipAllocationId` _parameter_, not a resource. CloudFormation does
disassociate-old + associate-new natively when the parameter changes — the
instance, its IAM profile, and its SSM agent are never touched. The entire
class of "drift" disappears, and so does this skill's previous bulk.

## One canonical invocation

```bash
gh workflow run deploy-edge-stage0.yml \
  -f edge_id=<id> \
  -f operation=rotate_egress_ip \
  -f confirm_stack=tokenkey-edge-<id>-stage0 \
  -f rotation_reason='<short reason>' \
  [-f candidate_allocation_id=eipalloc-XXXX]
```

`edge_id` matches a key in
[`deploy/aws/stage0/edge-targets.json`](../../../deploy/aws/stage0/edge-targets.json)
(normalize `edge-uk1` → `uk1`). `rotation_reason` is required and ends up on
the new EIP's `tokenkey:replaces-reason` tag and in the run summary's
`edge-polluted-ips.json` snippet.

`candidate_allocation_id` is optional. If unset, the workflow allocates a
fresh EIP and refuses any allocation that lands on a known-polluted IP. Set
it only when the operator has pre-vetted a specific allocation outside the
workflow (rare — only useful if the auto path keeps drawing dirty IPs).

What the workflow does, in order:
1. Reads the stack's current `EipAllocationId` (= rollback target).
2. Allocates a fresh EIP unless `candidate_allocation_id` is set; cross-checks against [`edge-polluted-ips.json`](../../../deploy/aws/stage0/edge-polluted-ips.json) and re-allocates if dirty.
3. `aws cloudformation deploy --parameter-overrides EipAllocationId=<new>` — atomic CFN swap.
4. Polls `ssm:DescribeInstanceInformation` until `PingStatus=Online` (post-mutation invariant; uk1-2026-05-22 was the incident that motivated this gate).
5. Runs the pollution probe via SSM **on the edge itself** (no throwaway nano): confirms outbound IP, then curls Anthropic / OpenAI / Google with dummy keys looking for `403 + Cloudflare HTML` (= polluted) vs `401/400 + provider-shaped JSON` (= clean).
6. On polluted → automatic revert (CFN update-stack back to OLD_ALLOC) + release the freshly-allocated EIP (only if the workflow itself allocated it).
7. On clean → curl `https://<domain>/health` via `--resolve <domain>:443:<new_ip>` to prove the data plane survives end-to-end on the new IP before DNS propagation.
8. Emits a step summary with: old/new IP, retired-IP JSON snippet ready to paste into [`edge-polluted-ips.json`](../../../deploy/aws/stage0/edge-polluted-ips.json), and the Porkbun A-record change to make.

## Two operator steps left (intentional)

1. **DNS at Porkbun (or your provider)**: change the A record for
   `api-<id>.tokenkey.dev` to the new IP. The workflow does not automate this
   because the Porkbun API token is not in repo secrets; the run summary
   prints the exact transition.
2. **Append the retired IP to
   [`edge-polluted-ips.json`](../../../deploy/aws/stage0/edge-polluted-ips.json)**
   so future rotations refuse to re-allocate it. The run summary prints a
   paste-ready JSON entry. After DNS has propagated (~1 hour) you may
   `aws ec2 release-address` the old allocation and set `released_on`.

Everything else is mechanized.

## First-time migration (per stack)

A stack that still has the v1 shape (ElasticIP + EIPAssociation with Retain,
no `EipAllocationId` parameter) cannot accept `operation=rotate_egress_ip`
yet. Migrate it once:

```bash
# Dry run (read-only):
bash deploy/aws/stage0/migrate-edge-eip-to-parameter.sh <edge_id>

# Apply (changes live CFN):
bash deploy/aws/stage0/migrate-edge-eip-to-parameter.sh <edge_id> --apply
```

The migration keeps the same physical EIP — the public IP does NOT change.
It only converts the CFN representation from "EIP is in the template" to "EIP
is referenced by allocation-id parameter". After the migration, the stack
accepts `operation=rotate_egress_ip` and the rest of this skill is in force.

## Stop-the-line rules

The workflow itself enforces the data-plane invariants. This skill must still
refuse when:

1. The normalized `edge_id` is not a key in
   [`deploy/aws/stage0/edge-targets.json`](../../../deploy/aws/stage0/edge-targets.json).
2. `rotation_reason` is empty or only whitespace.
3. The target stack has not been migrated yet (`describe-stacks` shows no
   `EipAllocationId` parameter) — direct the operator to
   `migrate-edge-eip-to-parameter.sh` first.
4. `operation=rotate_egress_ip` is requested against `tokenkey-prod-stage0`
   (the production gateway). Prod IP rotation has different blast radius
   (active client connections) and is intentionally not covered by this
   skill.

The workflow handles the rest as mechanical gates — operator does not need
this skill to babysit candidate allocation, probe results, or revert.

## Reporting contract

The workflow's step summary is the contract. Nothing else needs to be
produced. If you need to summarize for a chat caller, mirror the values from
the summary:

```text
edge_id: <id>
region: <aws-region>
old_ip / old_alloc: <ip> / <eipalloc-…>
new_ip / new_alloc: <ip> / <eipalloc-…>
status: rotated | reverted-polluted | revert-failed
follow_up:
  - update DNS A-record at Porkbun: <domain> → <new_ip>
  - append retired IP entry to deploy/aws/stage0/edge-polluted-ips.json
  - (after ~1h DNS propagation) aws ec2 release-address --allocation-id <old_alloc>
```

## Out of scope

- Production gateway IP rotation (`tokenkey-prod-stage0`).
- Cross-region "clean EIP pool" maintenance. If the auto path repeatedly
  draws polluted IPs in a region, the answer is a different region (or an
  upstream Trust & Safety ticket), not a pre-warmed pool — adding a pool is
  premature.
- DNS automation (Porkbun API). Documented as a known follow-up; would be a
  separate skill + a separate secret if/when wanted.

## v1 (legacy) reference

The previous procedure — throwaway-nano probe, manual `associate-address`,
drift-lock flag, `recover-drift` Phase 2 detach + IMPORT — is documented in
[`docs/deploy/tokenkey-edge-ip-history.md`](../../../docs/deploy/tokenkey-edge-ip-history.md).
After all edges have been migrated to the parameter shape, that document
becomes pure history.
