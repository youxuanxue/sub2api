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

Applies to this repo (TokenKey fork of sub2api). Goal: when an edge's egress IP is risk-blocked by an upstream API (or otherwise needs to change), replace it with a verified-clean candidate without breaking the CFN stack — and leave the stack in a state that can be brought back to IN_SYNC.

The prose runbook for the same procedure lives in [`docs/deploy/tokenkey-edge-ip-history.md`](../../docs/deploy/tokenkey-edge-ip-history.md). This skill is the agent-driven version: same steps, more guardrails on parameter passing and on stop-the-line checks.

Authority discipline follows the repo's `CLAUDE.md` (ARM-only, no `[skip ci]` in landing commits, dev-rules submodule order, etc.). The skill MUST refuse to skip any of those.

## Invocation

```text
/tokenkey-stage0-edge-ip-rotation edge_id=<id> [operation=full|detect|swap|recover-drift|status] [reason=<short string>] [candidate_count=4]
```

| Parameter | Meaning |
|---|---|
| `edge_id` | Target edge id from `deploy/aws/stage0/edge-targets.json` (e.g. `fra1`). |
| `operation=full` | Allocate candidates → probe → swap → DNS handoff → verify → drift-lock → record. **Stops before CFN drift recovery; that is its own operation.** |
| `operation=detect` | Allocate candidates + probe only. Used when the user wants to vet IP cleanliness in the region without committing to a swap. Releases the candidates at the end. |
| `operation=swap` | Skip candidate detection; user has already picked a clean EIP (must already exist as an allocation in the region) and just wants the associate + DNS handoff + verify. |
| `operation=recover-drift` | Run § 5 of the prose runbook against an edge that is currently `drift_locked: true`. Read-only Phase 1 try, then guided Phase 2 detach + IMPORT. Stops at every CFN mutation for explicit confirmation. |
| `operation=status` | Read-only. Run `scripts/edge-ip-status.sh --markdown`, show drift state, no AWS mutations. |
| `reason` | Short string saved into `edge-polluted-ips.json` notes and into the tag `tokenkey:replaces-reason` on the new EIP. Required for `full` and `swap`. |
| `candidate_count` | How many candidate EIPs to allocate (default 4; region quota default = 5; replacement uses 1 existing slot). |

Default routing if the user is ambiguous:

- "Replace edge-X IP, it's blocked" → `operation=full reason="upstream-api-risk-block-YYYY-MM-DD"`
- "Just give me clean IPs in eu-west-2 to choose from" → `operation=detect`
- "edge-uk1 drift recovery time" → `operation=recover-drift edge_id=uk1`
- "Where do our edge IPs stand right now?" → `operation=status`

## Stop-the-line rules (all operations)

The skill must `fail` (refuse to continue) when:

1. `edge_id` is not in `deploy/aws/stage0/edge-targets.json`.
2. The edge's CFN stack does not exist in the declared region (use `aws cloudformation describe-stacks`).
3. The user has not supplied a `reason` for `full` or `swap`.
4. The new candidate EIP matches any entry in `deploy/aws/stage0/edge-polluted-ips.json`. Release immediately, re-allocate, never silently retry past the candidate quota.
5. A pre-existing tmux/work session has uncommitted changes to `deploy/aws/stage0/edge-targets.json`, `deploy/aws/stage0/edge-polluted-ips.json`, or `deploy/aws/cloudformation/stage0-edge-ec2.yaml` (`git status --porcelain` returns these paths). Surface the diff and ask the user to commit or stash first — concurrent edits to the SoT will corrupt the drift-lock state.
6. The selected candidate's outbound pollution-probe shows any `403` with Cloudflare HTML — never silently demote to "let's still use it because the others were worse".
7. For `recover-drift`: the edge is NOT actually drift-locked (`edge-targets.json[edge].drift_locked != true`). Confirm with the user before forcing the recovery procedure on a clean stack.

## Operation = `status`

Pure read. Mirror what would land in § 1 / § 2 of the doc.

```bash
bash scripts/edge-ip-status.sh --markdown
bash scripts/edge-ip-status.sh --json | jq '.active[] | select(.drift_locked == true)'
```

Report:

- Markdown tables verbatim.
- A one-line summary of drift-locked edges.
- A reminder of the next required action per drift-locked edge (recovery procedure).

## Operation = `detect` / `full` / `swap` — shared step contracts

### A. Resolve edge facts

```bash
REGION=$(jq -r ".targets.${edge_id}.region" deploy/aws/stage0/edge-targets.json)
STACK=$(jq -r ".targets.${edge_id}.stack"  deploy/aws/stage0/edge-targets.json)
DOMAIN=$(jq -r ".targets.${edge_id}.domain" deploy/aws/stage0/edge-targets.json)
EDGE_INSTANCE=$(aws cloudformation describe-stack-resources --region "$REGION" --stack-name "$STACK" \
  --query 'StackResources[?LogicalResourceId==`Instance`].PhysicalResourceId' --output text)
OLD_ALLOC=$(aws ec2 describe-addresses --region "$REGION" \
  --filters "Name=instance-id,Values=$EDGE_INSTANCE" \
  --query 'Addresses[0].AllocationId' --output text)
OLD_IP=$(aws ec2 describe-addresses --region "$REGION" \
  --allocation-ids "$OLD_ALLOC" --query 'Addresses[0].PublicIp' --output text)
```

Report what was found before any mutation.

### B. Candidate allocation (`detect`, `full`)

```bash
for i in $(seq 1 $candidate_count); do
  aws ec2 allocate-address --region "$REGION" --domain vpc \
    --tag-specifications "ResourceType=elastic-ip,Tags=[{Key=Name,Value=${edge_id}-candidate-${i}},{Key=tokenkey:purpose,Value=ip-replacement-$(date -u +%F)}]" \
    --query '{IP:PublicIp,Alloc:AllocationId}' --output json
done
```

If any IP is in `edge-polluted-ips.json`, immediately `release-address` and re-allocate. Stop after `candidate_count * 2` total allocate attempts to bound cost (1 cent / hr per EIP × 8 = trivial, but bound it anyway).

### C. Probe instance launch

Reuse the edge's IAM profile, subnet, SG so SSM works without IAM work:

```bash
EDGE_IAM_PROFILE=$(aws ec2 describe-instances --region "$REGION" --instance-ids "$EDGE_INSTANCE" \
  --query 'Reservations[0].Instances[0].IamInstanceProfile.Arn' --output text | awk -F/ '{print $NF}')
EDGE_SUBNET=$(aws ec2 describe-instances --region "$REGION" --instance-ids "$EDGE_INSTANCE" \
  --query 'Reservations[0].Instances[0].SubnetId' --output text)
EDGE_SG=$(aws ec2 describe-instances --region "$REGION" --instance-ids "$EDGE_INSTANCE" \
  --query 'Reservations[0].Instances[0].SecurityGroups[0].GroupId' --output text)
AL2023_ARM_AMI=$(aws ec2 describe-images --region "$REGION" --owners amazon \
  --filters "Name=name,Values=al2023-ami-2023.*-arm64" "Name=state,Values=available" \
            "Name=architecture,Values=arm64" \
  --query 'sort_by(Images,&CreationDate)[-1].ImageId' --output text)

PROBE=$(aws ec2 run-instances --region "$REGION" \
  --image-id "$AL2023_ARM_AMI" --instance-type t4g.nano \
  --iam-instance-profile "Name=$EDGE_IAM_PROFILE" \
  --subnet-id "$EDGE_SUBNET" --security-group-ids "$EDGE_SG" \
  --associate-public-ip-address \
  --metadata-options "HttpTokens=required,HttpEndpoint=enabled" \
  --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${edge_id}-ip-probe-$(date -u +%F)},{Key=tokenkey:purpose,Value=ip-replacement-$(date -u +%F)}]" \
  --query 'Instances[0].InstanceId' --output text)

# wait for SSM
until aws ssm describe-instance-information --region "$REGION" \
  --filters "Key=InstanceIds,Values=$PROBE" \
  --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null | grep -q Online; do
  sleep 5
done
```

### D. Pollution probe per candidate

For each candidate `(IP, AllocId)`:

1. `aws ec2 associate-address --region "$REGION" --instance-id "$PROBE" --allocation-id "$AllocId" --allow-reassociation`
2. Sleep ~10 s for the EIP to actually take effect.
3. `aws ssm send-command` running an inline check that:
   - asserts outbound IP via `curl https://api.ipify.org` matches the expected candidate (else the EIP did not propagate; retry once, else fail this candidate);
   - probes Anthropic `POST /v1/messages`, OpenAI `POST /v1/chat/completions`, Google `GET /v1beta/models?key=dummy` and prints `http_code` + first 240 bytes of body;
4. Mark **pass** only if all three are application-layer 401/400 (provider JSON). Any 403 with Cloudflare HTML body = polluted; mark and skip.

Report a per-candidate verdict table.

### E. Stop point for `detect`

Print the verdict table, release the probe + every candidate that the user does **not** want to keep, and exit. Do not mutate the edge.

### F. Live swap (`full`, `swap`)

Confirm with the user *which* candidate to use if more than one is clean. Then:

```bash
NEW_ALLOC=<chosen>
NEW_IP=<chosen>

aws ec2 associate-address --region "$REGION" \
  --instance-id "$EDGE_INSTANCE" --allocation-id "$NEW_ALLOC" --allow-reassociation

aws ec2 create-tags --region "$REGION" --resources "$NEW_ALLOC" \
  --tags "Key=Name,Value=tokenkey-${edge_id}-eip" \
         "Key=tokenkey:status,Value=active" \
         "Key=tokenkey:replaced-on,Value=$(date -u +%F)" \
         "Key=tokenkey:replaces,Value=${OLD_IP}" \
         "Key=tokenkey:replaces-reason,Value=${reason}"
```

### G. DNS handoff (HUMAN STEP — must pause)

The repo currently has no Porkbun automation. The skill MUST pause and instruct the user to:

> Go to https://porkbun.com and update the A record for `<DOMAIN>` from `<OLD_IP>` to `<NEW_IP>`. Set TTL=60s. Confirm here when done.

Wait for explicit confirmation. Do not poll DNS automatically — that creates unbounded loops if the user defers. After confirmation, the skill verifies propagation across `8.8.8.8 / 1.1.1.1` independently.

If a future Porkbun-API credential becomes available in `~/.porkbun.env` or SSM, this step can become automated — but DO NOT introduce that credential as part of a rotation; it is a separate piece of infrastructure work.

### H. Verify from an independent observation point

Local macOS routinely hijacks outbound to public IPs via VPN/proxy/Docker — `remote_ip=127.0.0.1` is the classic symptom. Verify from the probe instance, which is still alive and on a candidate EIP that is NOT the new live one (so its `curl` actually leaves the AWS network and re-enters at the new EIP):

```bash
aws ssm send-command --region "$REGION" --instance-ids "$PROBE" \
  --document-name AWS-RunShellScript \
  --parameters 'commands=["curl -sS --max-time 12 -o /tmp/r -w \"http=%{http_code} ip=%{remote_ip}\\n\" https://<edge-domain>/robots.txt"]'
```

Pass criterion: `http=200 ip=<NEW_IP>` and the response body matches the edge's expected content.

### I. Drift-lock the edge

```bash
jq --arg edge "$edge_id" \
   --arg reason "EIP replaced $(date -u +%F) outside CFN (${OLD_IP} polluted → ${NEW_IP}); recovery in docs/deploy/tokenkey-edge-ip-history.md § 5 required before next deploy." \
   '.targets[$edge].drift_locked = true | .targets[$edge].drift_reason = $reason' \
   deploy/aws/stage0/edge-targets.json > /tmp/m && mv /tmp/m deploy/aws/stage0/edge-targets.json
```

Then update `deploy/aws/stage0/edge-polluted-ips.json` with the new entry (region, retired_on, released_on once released, previous_edge, notes including the user-supplied `reason`).

### J. Cleanup

- `aws ec2 terminate-instances --instance-ids "$PROBE"`.
- `aws ec2 release-address` every unused candidate.
- `aws ec2 release-address` the old polluted EIP immediately (default policy is "release on confirm, do not keep 24h"; the EIP is on the permanent excluded list now so an accidental re-allocation will be caught by stop-the-line rule #4 next time).
- Run `scripts/edge-ip-status.sh --markdown` and paste the regenerated tables back into § 1 / § 2 of `docs/deploy/tokenkey-edge-ip-history.md`.

### K. PR

Open a single PR containing:

- `deploy/aws/stage0/edge-targets.json` — `drift_locked: true` set on the edge.
- `deploy/aws/stage0/edge-polluted-ips.json` — new pollution entry.
- `docs/deploy/tokenkey-edge-ip-history.md` — regenerated § 1 / § 2 tables; § 3 updated.

Commit message MUST include `no-web-impact` (this is metadata + docs only — no Go / frontend impact, and `scripts/preflight.sh` will fail otherwise).

Title shape: `chore(stage0-edge): rotate edge-<id> EIP <old> → <new> + drift-lock`.

PR body MUST link the prose runbook § 5 and explicitly note: **the next required action is the CFN drift recovery (`operation=recover-drift`); do not run `deploy-edge-stage0.yml` against this edge until that PR has merged.**

## Operation = `recover-drift`

The full procedure is in [`docs/deploy/tokenkey-edge-ip-history.md`](../../docs/deploy/tokenkey-edge-ip-history.md) § 5. The skill MUST:

1. Refuse unless the edge is currently `drift_locked: true`.
2. Resolve `REGION`, `STACK`, current live EIP / association IDs by querying AWS (not by trusting the doc).
3. Phase 1 — apply Retain template with `UsePreviousValue` parameters. Wait. Treat both completion and rollback as expected; never `--no-rollback`.
4. Phase 2 — pause and explicitly ask the user to confirm before each of:
   - the detach `update-stack` (temporary template with `ElasticIP`/`EIPAssoc` commented out);
   - the IMPORT `create-change-set` (against the committed template);
   - the change-set diff review (skill prints `describe-change-set`);
   - the `execute-change-set`.
5. Verify `detect-stack-drift` returns IN_SYNC.
6. Open a follow-up PR clearing `drift_locked` on the edge.

The skill MUST NOT touch the committed template during Phase 2's detach step. It generates `/tmp/stage0-edge-ec2.detach.yaml` from the committed template via comment-out, applies it, then never references the temp file again. Restoring is automatic because the IMPORT change-set is created against the committed template, not the temp.

## Reporting back to the user

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

## Known failure patterns

- `AddressLimitExceeded` on candidate allocation → region EIP quota too tight. Either lower `candidate_count`, request a quota increase, or release any orphan EIPs in the region first.
- All candidates show the pollution signal → the region's EIP pool is dirty for this provider. Switch to a less-used region for this specific edge, or open a quota / Trust & Safety ticket with the upstream provider. Do not give up and use a polluted IP.
- SSM agent never goes Online for the probe → the IAM profile is missing `AmazonSSMManagedInstanceCore` (atypical for `tokenkey-edge-*-stage0` stacks — investigate before reusing).
- Phase 2 IMPORT change-set fails with "resource identifier shape" error → AWS IMPORT spec changed. Re-fetch `aws-resource-ec2-eip.html` / `aws-properties-ec2-eip-association.html`, update the "Resource-identifier reference" table in the prose doc, and re-create the change-set.
- Phase 2 IMPORT change-set includes resources other than `ElasticIP` / `EIPAssoc` → STOP. The detach step touched more than it should have, or someone committed unrelated template changes between detach and import. Investigate before executing.

## Out of scope

- Anything touching prod (`tokenkey-prod-stage0`). This skill is edge-only. Prod IP rotation has different blast radius (active client connections from end-users) and a different runbook.
- DNS automation. Until Porkbun credentials become available, step G stays human-paced.
- Cross-region pollution scoring / clean-pool maintenance. If we end up needing a pool of pre-verified clean EIPs per region, that's a separate skill and a separate piece of automation.
