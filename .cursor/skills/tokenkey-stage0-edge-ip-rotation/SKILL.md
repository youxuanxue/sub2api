---
name: tokenkey-stage0-edge-ip-rotation
description: >-
  Rotate / replace the egress Elastic IP of an existing TokenKey Stage0 edge
  (uk1/us1/sg1/fra1/…) when the live IP has been risk-blocked ("polluted") by
  an upstream API (Anthropic / OpenAI / Google). Drives candidate-allocation,
  active-pollution probing from a throwaway t4g.nano, the live EIP swap, DNS
  update at Porkbun (human step), service verification from an independent
  observation point, mechanical drift-lock of the edge, and the follow-up
  CloudFormation drift recovery (detach + IMPORT). Validated end-to-end on
  edge-uk1 2026-05-20.
---

# TokenKey: rotate an edge gateway's egress EIP

This skill is the **agent execution contract** on top of the prose runbook in
[`docs/deploy/tokenkey-edge-ip-history.md`](../../docs/deploy/tokenkey-edge-ip-history.md).
That doc is the **single source of truth** for the procedure (bash commands,
step ordering, AWS flags). This file does NOT duplicate those steps — it only
defines:

- the agent invocation surface (`/tokenkey-stage0-edge-ip-rotation …`),
- which doc sections each operation drives,
- agent-specific stop-the-line refuse rules,
- the structured report the agent returns at the end,
- known failure patterns and out-of-scope boundaries.

When the doc updates a command, this skill picks up the change automatically —
do not paste bash blocks here.

Authority discipline follows the repo's `CLAUDE.md` (ARM-only, no `[skip ci]`
in landing commits, dev-rules submodule order, etc.). The skill MUST refuse to
skip any of those.

## Invocation

```text
/tokenkey-stage0-edge-ip-rotation edge_id=<id> [operation=full|detect|swap|recover-drift|status] [reason=<short string>] [candidate_count=4]
```

| Parameter | Meaning |
|---|---|
| `edge_id` | Target edge id from `deploy/aws/stage0/edge-targets.json` (e.g. `fra1`). |
| `operation=full` | Default. Allocate candidates → probe → swap → DNS handoff → verify → drift-lock → record. **Stops before CFN drift recovery; that is its own operation.** |
| `operation=detect` | Allocate candidates + probe only. Used when the user wants to vet IP cleanliness in the region without committing to a swap. Releases the candidates at the end. |
| `operation=swap` | Skip candidate detection; user has already picked a clean EIP (must already exist as an allocation in the region) and just wants the associate + DNS handoff + verify. |
| `operation=recover-drift` | Run § 5 of the prose runbook against an edge that is currently `drift_locked: true`. Phase 1 try, then Phase 2 detach + IMPORT. Stops at every CFN mutation for explicit confirmation. |
| `operation=status` | Read-only. Run `scripts/edge-ip-status.sh --markdown`, show drift state, no AWS mutations. |
| `reason` | Short string saved into `edge-polluted-ips.json` notes and into the tag `tokenkey:replaces-reason` on the new EIP. Required for `full` and `swap`. |
| `candidate_count` | How many candidate EIPs to allocate (default 4; region quota default = 5; replacement uses 1 existing slot). |

Default routing if the user is ambiguous:

- "Replace edge-X IP, it's blocked" → `operation=full reason="upstream-api-risk-block-YYYY-MM-DD"`
- "Just give me clean IPs in eu-west-2 to choose from" → `operation=detect`
- "edge-uk1 drift recovery time" → `operation=recover-drift edge_id=uk1`
- "Where do our edge IPs stand right now?" → `operation=status`

## Operation routing (which doc sections to drive)

Drive the doc literally — do not improvise commands. Where the doc shows a
parameter substitution (`$REGION`, `$EDGE_INSTANCE`, …), resolve it from
`deploy/aws/stage0/edge-targets.json` + live AWS state before running the step.

| Operation | Doc sections to drive | Notes |
|---|---|---|
| `full` | § 4 steps 1–11 (in order) | Step 7 (DNS at Porkbun) is a HARD HUMAN PAUSE — see Stop-the-line rule 8. Step 11 (CFN drift recovery) is NOT auto-chained; surface it as `next_action` and exit. |
| `detect` | § 4 steps 2–4, then release every allocated candidate | Skip steps 5–11 entirely. The probe instance and all candidates are torn down before exit unless the user names specific allocations to keep. |
| `swap` | § 4 steps 5–10 | Skip steps 1–4; the user names the clean allocation up front. Validate the named allocation is NOT in `edge-polluted-ips.json` before step 5. |
| `recover-drift` | § 5 Phase 1, then Phase 2 steps 1–8, including § 5 caveats and "Choosing the template basis" decision | Refuse unless `edge-targets.json[edge].drift_locked == true`. Re-query AWS for live `NEW_ALLOC` / `NEW_ASSOC` — do not trust doc § 1 / § 2 tables. Before Phase 2 step 1, fetch the deployed template (`aws cloudformation get-template --output json \| jq -r '.TemplateBody'`) and `diff` against `deploy/aws/cloudformation/stage0-edge-ec2.yaml`; if the diff is non-trivial (new SSM resources, UserData changes, Outputs changes), drive the procedure from the deployed-template basis per doc § 5 "Choosing the template basis". Own the AMI-pin lifecycle: put-parameter `/tokenkey/edge/<edge>/stage0/recovery/ami-pin` at the start of Phase 2, delete-parameter at the end after `IMPORT_COMPLETE` is verified. |
| `status` | (none — invokes the generator directly) | `bash scripts/edge-ip-status.sh --markdown` and `--json \| jq '.active[] \| select(.drift_locked == true)'`. Report verbatim plus a one-line summary of drift-locked edges and their next required action. |

For each candidate during § 4 step 4, return a per-candidate verdict table to
the user (one row per candidate: IP, Anthropic code, OpenAI code, Google code,
verdict = pass / polluted). The doc shows the per-probe curl; this table is
how the agent surfaces aggregate results.

## Stop-the-line rules (all operations)

The skill must `fail` (refuse to continue) when:

1. `edge_id` is not in `deploy/aws/stage0/edge-targets.json`.
2. The edge's CFN stack does not exist in the declared region (use `aws cloudformation describe-stacks`).
3. The user has not supplied a `reason` for `full` or `swap`.
4. A candidate EIP (newly allocated, or user-named for `swap`) matches any entry in `deploy/aws/stage0/edge-polluted-ips.json`. Release immediately, re-allocate (`full`/`detect`) or reject the user's pick (`swap`); never silently retry past the candidate quota.
5. A pre-existing tmux/work session has uncommitted changes to `deploy/aws/stage0/edge-targets.json`, `deploy/aws/stage0/edge-polluted-ips.json`, or `deploy/aws/cloudformation/stage0-edge-ec2.yaml` (`git status --porcelain` returns these paths). Surface the diff and ask the user to commit or stash first — concurrent edits to the SoT will corrupt the drift-lock state.
6. The selected candidate's outbound pollution-probe shows any `403` with Cloudflare HTML — never silently demote to "let's still use it because the others were worse".
7. For `recover-drift`: the edge is NOT actually drift-locked. Confirm with the user before forcing the recovery procedure on a clean stack. Phase 2 step 1 MUST write `/tmp/stage0-edge-ec2.detach.yaml` to /tmp (NEVER edit the in-tree committed template; doing so would violate stop-the-line rule 5 on its own next run). The detach template's BASIS may be either the in-tree committed template OR the live deployed template fetched via `get-template` — pick per doc § 5 "Choosing the template basis for Phase 2" (deployed when significant template drift has accumulated since stack creation).
8. § 4 step 7 (Porkbun DNS update) is a HARD HUMAN PAUSE. The skill prints the OLD_IP → NEW_IP transition and the domain, then waits for explicit user confirmation. Do not poll DNS automatically — that creates unbounded loops if the user defers. After confirmation, run § 4 step 8 verification from the probe instance.
9. For `recover-drift` Phase 2: STOP and ask the user to confirm before each of `update-stack` (detach), `create-change-set` (IMPORT), and `execute-change-set`. The change-set diff (`describe-change-set`) MUST show only `ElasticIP` / `EIPAssoc` with action `Import` — any other resource appearing is an immediate stop.

## Reporting contract

Every operation returns a structured summary at the end:

```text
edge_id: <id>
region: <aws-region>
operation: <full|detect|swap|recover-drift|status>
old_ip: <ip or n/a>
new_ip: <ip or n/a>
new_allocation_id: <eipalloc-…>
new_association_id: <eipassoc-…>
drift_locked: true|false
candidates_allocated: <n>
candidates_polluted: <n>  # number that failed the probe
probe_instance_id: <i-…>  # or terminated:<i-…>
next_action: <one line — e.g. "human DNS handoff", "run operation=recover-drift", "merge PR #N">
pr: <url or "not opened">
```

For `full` / `swap`: the PR shape (title, body checklist, required commit
markers `no-web-impact` and `no-upstream-touch`) is described in doc § 4
step 10 and in `CLAUDE.md` PR Checklist. The PR body MUST explicitly note:
**the next required action is the CFN drift recovery (`operation=recover-drift`);
do not run `deploy-edge-stage0.yml` against this edge until that PR has merged.**

## Known failure patterns

- `AddressLimitExceeded` on candidate allocation → region EIP quota too tight. Either lower `candidate_count`, request a quota increase, or release any orphan EIPs in the region first.
- All candidates show the pollution signal → the region's EIP pool is dirty for this provider. Switch to a less-used region for this specific edge, or open a quota / Trust & Safety ticket with the upstream provider. Do not give up and use a polluted IP.
- SSM agent never goes Online for the probe → the IAM profile is missing `AmazonSSMManagedInstanceCore` (atypical for `tokenkey-edge-*-stage0` stacks — investigate before reusing).
- Phase 2 IMPORT change-set fails with "resource identifier shape" error → AWS IMPORT spec changed. Re-fetch the AWS docs URLs in doc § 5 "Resource-identifier reference", update the table in the prose doc, and re-create the change-set.
- Phase 2 IMPORT change-set includes resources other than `ElasticIP` / `EIPAssoc` → STOP. The detach step touched more than it should have, or someone committed unrelated template changes between detach and import. Investigate before executing.

## Out of scope

- Anything touching prod (`tokenkey-prod-stage0`). This skill is edge-only. Prod IP rotation has different blast radius (active client connections from end-users) and a different runbook.
- DNS automation. Until Porkbun credentials become available, § 4 step 7 stays human-paced.
- Cross-region pollution scoring / clean-pool maintenance. If we end up needing a pool of pre-verified clean EIPs per region, that is a separate skill and a separate piece of automation.
