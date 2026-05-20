# Edge Egress IP History & Replacement Runbook

This document tracks the egress (Elastic IP) lifecycle of TokenKey's AWS Stage0 edge gateways: which IPs are currently active, which have been retired (typically because an upstream API risk-blocked them — referred to here as "pollution"), and the validated procedure for replacing a polluted IP without breaking the CFN stack.

Companion to [`deploy/aws/stage0/edge-targets.json`](../../deploy/aws/stage0/edge-targets.json) (edge inventory) and [`deploy/aws/cloudformation/stage0-edge-ec2.yaml`](../../deploy/aws/cloudformation/stage0-edge-ec2.yaml) (edge CFN template).

---

## 1. Active edge EIPs

| Edge | Region | EIP | AllocationId | Active since | Instance |
| --- | --- | --- | --- | --- | --- |
| edge-uk1 | eu-west-2 | `35.177.124.150` | `eipalloc-0f7da5f311cc36075` | 2026-05-20 | `i-0f6ece892c918ea9a` |
| edge-us1 | us-west-2 | _(not yet deployed)_ | — | — | — |
| edge-sg1 | ap-southeast-1 | _(not yet deployed)_ | — | — | — |
| edge-fra1 | eu-west-3 | _(not yet deployed)_ | — | — | — |

Update this table on every EIP swap.

## 2. Permanently excluded IPs (pollution history)

IPs in this list have been observed triggering upstream API risk-blocks (Anthropic / OpenAI / Google, or any other production-critical provider). Any future `allocate-address` call that returns one of these MUST immediately `release-address` and re-allocate. Never bind them to a TokenKey edge instance again.

| IP | Region | Retired on | Released on | Previously edge | Notes |
| --- | --- | --- | --- | --- | --- |
| `3.9.160.161` | eu-west-2 | 2026-05-20 | 2026-05-20 | edge-uk1 | First documented pollution. User-reported upstream-API risk-block. |

Add to this table the moment a replacement runs, not after the fact.

## 3. CFN drift — current state

Until the recovery runbook in § 5 is executed, the edge-uk1 stack carries known drift:

- CFN-managed `ElasticIP` resource still references the now-released `3.9.160.161`.
- CFN-managed `EIPAssoc` resource references the original association id; the live association (created by `associate-address --allow-reassociation` outside CFN) is `eipassoc-011059cc27c15b401`.

**Implication:** running `deploy-edge-stage0.yml` (or any other `update-stack`) against edge-uk1 in the current drifted state can:
- attempt to recreate / re-reference the released `3.9.160.161` (re-introducing pollution), OR
- fail mid-update with `ResourceNotFound`.

**Do NOT run `deploy-edge-stage0.yml` against edge-uk1 until § 5 has been executed once.**

## 4. Replacement runbook (when a current IP is polluted)

Validated end-to-end against eu-west-2 / edge-uk1 on 2026-05-20.

### Preconditions

- AWS CLI configured for the target region.
- SSM access — the edge instance's IAM profile must include `AmazonSSMManagedInstanceCore` (it does for `tokenkey-edge-*-stage0` stacks).
- Account-level EIP quota in the region (AWS default = 5). Replacement uses 1–4 candidate slots above the existing footprint; if the quota is tight, request an increase or re-allocate in smaller batches.

### Steps

1. **(Optional) Lower DNS TTL** on the edge's domain at the DNS provider (Porkbun today) to 60 s, one TTL cycle before the switch. Skipping this means up to ~10 min of stale external resolution after the switch.

2. **Allocate candidate EIPs** in the target region, but do not associate yet. One per available quota slot; 4 is enough in practice.
   ```bash
   aws ec2 allocate-address --region "$REGION" --domain vpc \
     --tag-specifications 'ResourceType=elastic-ip,Tags=[{Key=Name,Value=<edge>-candidate-N},{Key=tokenkey:purpose,Value=ip-replacement-YYYY-MM-DD}]'
   ```
   If any candidate matches an entry in § 2, immediately `release-address` and re-allocate.

3. **Launch a t4g.nano probe** in the same VPC/subnet/SG as the edge instance, reusing the edge's IAM profile (so SSM works out of the box). AMI = latest AL2023 ARM64. The probe is short-lived (< 30 min).
   ```bash
   aws ec2 run-instances --region "$REGION" \
     --image-id "$AL2023_ARM_AMI" \
     --instance-type t4g.nano \
     --iam-instance-profile "Name=$EDGE_IAM_PROFILE" \
     --subnet-id "$EDGE_SUBNET" --security-group-ids "$EDGE_SG" \
     --associate-public-ip-address \
     --metadata-options "HttpTokens=required,HttpEndpoint=enabled" \
     --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=<edge>-ip-probe-YYYY-MM-DD}]'
   ```
   Wait for SSM `PingStatus=Online` (typically 60–120 s).

4. **Pollution detection.** The canonical pass signal is HTTP 401 / 400 with provider-shaped error JSON. The canonical fail signal is HTTP 403 with a Cloudflare challenge HTML body. For each candidate:
   - `aws ec2 associate-address --instance-id "$PROBE" --allocation-id "$CAND" --allow-reassociation`
   - Wait ~10 s for EIP propagation.
   - `aws ssm send-command` running:
     ```bash
     # Confirm outbound IP first
     test "$(curl -s --max-time 10 https://api.ipify.org)" = "$EXPECTED_IP" || exit 1

     # Then probe each upstream
     curl -sS --max-time 15 -o /tmp/_b -w '%{http_code}\n' \
       -H 'x-api-key: dummy' \
       -X POST -H 'content-type: application/json' \
       --data '{"model":"claude-sonnet-4-6","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}' \
       https://api.anthropic.com/v1/messages

     curl -sS --max-time 15 -o /tmp/_b -w '%{http_code}\n' \
       -H 'authorization: Bearer dummy' \
       -X POST -H 'content-type: application/json' \
       --data '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}' \
       https://api.openai.com/v1/chat/completions

     curl -sS --max-time 15 -o /tmp/_b -w '%{http_code}\n' \
       'https://generativelanguage.googleapis.com/v1beta/models?key=dummy'
     ```
   - Pass = all three application-layer 401/400. Any 403 + Cloudflare HTML in the body = polluted.

5. **Switch the live EIP**:
   ```bash
   aws ec2 associate-address --region "$REGION" \
     --instance-id "$EDGE_INSTANCE" --allocation-id "$CHOSEN_CAND" --allow-reassociation
   ```
   The old EIP is automatically `disassociate`d but **retained** in the account (no auto-release).

6. **Update DNS** at the provider so the edge domain points at the new EIP.

7. **Verify from an independent observation point.** Do NOT rely on `curl` from a laptop — many local setups (VPN, Docker, mitmproxy, Charles, transparent proxies) silently hijack outbound to public IPs even with `--resolve`, producing `remote_ip=127.0.0.1`. The simplest reliable verifier is the probe instance, still running from step 3:
   ```bash
   aws ssm send-command --region "$REGION" --instance-ids "$PROBE" \
     --document-name AWS-RunShellScript \
     --parameters 'commands=["curl -sS --max-time 12 -o /tmp/r -w \"http=%{http_code} ip=%{remote_ip}\\n\" https://<edge-domain>/robots.txt"]'
   ```
   Expect `http=200 ip=<NEW_EIP>` and a sensible response body.

8. **Cleanup**:
   - `aws ec2 terminate-instances --instance-ids "$PROBE"`.
   - `aws ec2 release-address` every unused candidate EIP.
   - The old (polluted) EIP can be retained 24 h as rollback insurance, but the rollback target is gone the moment another AWS customer in the region's pool grabs it. Default: release immediately and add the IP to § 2.
   - Re-tag the new EIP to `<project>-<edge>-eip` so it matches the template's expected Tag-Name pattern.

9. **CFN drift recovery** — every EIP replacement done outside the template introduces drift. See § 5.

## 5. CFN drift recovery runbook

The Stage0 edge template (`deploy/aws/cloudformation/stage0-edge-ec2.yaml`) creates both `ElasticIP` and `EIPAssoc` via CFN. The replacement procedure in § 4 swaps them at the AWS layer — CFN's view stays frozen on the old physical IDs.

### What the accompanying template change introduces

The template now adds `Retain` policies so that future `update-stack` / `delete-stack` operations cannot accidentally destroy a manually-replaced EIP:

```yaml
  ElasticIP:
    Type: AWS::EC2::EIP
    DependsOn: IGWAttach
    DeletionPolicy: Retain          # never auto-release on stack delete/replace
    UpdateReplacePolicy: Retain     # never auto-release on resource update
    Properties:
      Domain: vpc
      Tags: [...]

  EIPAssoc:
    Type: AWS::EC2::EIPAssociation
    DeletionPolicy: Retain
    UpdateReplacePolicy: Retain
    Properties:
      AllocationId: !GetAtt ElasticIP.AllocationId
      InstanceId: !Ref Instance
```

With these policies in place, future EIP replacements (§ 4) no longer destroy data on the next stack update — CFN still drifts, but stack operations stop being destructive. The IMPORT phase below is still required to fully realign CFN's view with reality.

### Recovery procedure (one-shot, per drifted edge)

For edge-uk1 specifically, this must be executed exactly once before the next `deploy-edge-stage0.yml` run against uk1.

**Phase 1 — apply this PR's template (Retain policy now in place):**

```bash
aws cloudformation update-stack --region eu-west-2 \
  --stack-name tokenkey-edge-uk1-stage0 \
  --template-body file://deploy/aws/cloudformation/stage0-edge-ec2.yaml \
  --capabilities CAPABILITY_IAM CAPABILITY_NAMED_IAM \
  --parameters file://deploy/aws/cloudformation/edge-uk1-params.json
```

This may fail with `ResourceNotFound` because the template-managed `ElasticIP` references the released `3.9.160.161`. If so, skip to Phase 2 directly — the IMPORT pattern there reseats the resources without needing Phase 1 to succeed.

**Phase 2 — IMPORT the live `35.177.124.150` + current association:**

1. Prepare `resources-to-import.json`:
   ```json
   [
     {
       "ResourceType": "AWS::EC2::EIP",
       "LogicalResourceId": "ElasticIP",
       "ResourceIdentifier": { "AllocationId": "eipalloc-0f7da5f311cc36075" }
     },
     {
       "ResourceType": "AWS::EC2::EIPAssociation",
       "LogicalResourceId": "EIPAssoc",
       "ResourceIdentifier": { "Id": "eipassoc-011059cc27c15b401" }
     }
   ]
   ```

2. Create a change-set of type `IMPORT`:
   ```bash
   aws cloudformation create-change-set --region eu-west-2 \
     --stack-name tokenkey-edge-uk1-stage0 \
     --change-set-name eip-import-2026-05-20 \
     --change-set-type IMPORT \
     --resources-to-import file://resources-to-import.json \
     --template-body file://deploy/aws/cloudformation/stage0-edge-ec2.yaml \
     --capabilities CAPABILITY_IAM CAPABILITY_NAMED_IAM
   ```

3. **Review the change-set diff carefully** with `describe-change-set`. Confirm: only `ElasticIP` and `EIPAssoc` are touched, no other resource is being replaced or deleted.

4. Execute:
   ```bash
   aws cloudformation execute-change-set --region eu-west-2 \
     --stack-name tokenkey-edge-uk1-stage0 \
     --change-set-name eip-import-2026-05-20
   ```

5. Verify:
   ```bash
   aws cloudformation detect-stack-drift --region eu-west-2 --stack-name tokenkey-edge-uk1-stage0
   # then poll describe-stack-drift-detection-status; expect IN_SYNC
   aws cloudformation describe-stack-resources --region eu-west-2 \
     --stack-name tokenkey-edge-uk1-stage0 \
     --query 'StackResources[?ResourceType==`AWS::EC2::EIP` || ResourceType==`AWS::EC2::EIPAssociation`]'
   ```
   Confirm `ElasticIP` now reports physical id `35.177.124.150` and `EIPAssoc` reports `eipassoc-011059cc27c15b401`.

6. Smoke the edge once more via the probe pattern in § 4 step 7 (or via `deploy-edge-stage0.yml` smoke-only mode) before reactivating that edge in the routing pool.

> **Operate Phase 2 as a deliberate human action, not from automation.** It rewires CFN ownership of production resources. If the change-set diff shows anything beyond ElasticIP / EIPAssoc, stop and re-evaluate.

### Long-term

A future refactor MAY pull `ElasticIP` + `EIPAssoc` out of `stage0-edge-ec2.yaml` and manage them in a separate, edge-local "external EIP" stack so the EIP lifecycle (volatile, replaced on pollution) is decoupled from the edge stack lifecycle (long-lived). Tracked separately; not part of this runbook.
