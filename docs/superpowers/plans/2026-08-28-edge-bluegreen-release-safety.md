# Edge Blue/Green Release Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make prod and Lightsail Edge share one fail-safe same-host blue/green deploy primitive and select the Edge canary from measured capacity and recent traffic.

**Architecture:** `ops/stage0/deploy_via_ssm_bluegreen.sh` remains the sole deploy state machine; the managed-instance ID selects only a prod or Edge environment profile. A new read-only Edge release probe emits one strict JSON record per host, and `scripts/stage0/pick_release_canary_edge.py` probes the full fleet before applying the approved deterministic sort.

**Tech Stack:** Bash, AWS SSM, Docker Compose, Caddy, Python `unittest`, GitHub Actions YAML.

**Spec:** `docs/approved/deploy-stage0-workflow.md`

## Global Constraints

- Externally meaningful deploy state is only `pre_cutover` or `committed`.
- A pre-cutover failure removes the failed candidate and restores the saved `.env`; a committed failure never automatically switches Caddy back.
- Edge admission requires `MemAvailable >= max(320 MiB, active app working set + 128 MiB)` and at least 5 GiB free on `/`.
- Candidate `/health` owns protocol readiness; deployment code must not infer or probe protocol support.
- Steady state runs one app color; post-commit routed `/health` must pass for 30 seconds before stopping the previous app.
- Canary ordering is lowest completed requests over 30 minutes, greatest `MemAvailable` headroom, then matrix order; OAuth/Kiro count is audit-only.
- `scripts/checks/bluegreen-migration-safety.py` owns target-tag/range resolution and SQL compatibility for both prod and Edge workflows.
- `ops/stage0/bluegreen-capacity-policy.env` owns the three capacity constants consumed by deploy, probe, and picker.
- A colored Caddy route without `active-color` is explicit recovery state: restart starts both colors and deploy blocks until repaired.
- Current operational docs point to the shared owners; historical documents are retained only when marked superseded.
- No permanent dual instances, second Edge deploy state machine, new load balancer, automatic post-commit rollback, or upstream account probing.

---

### Task 1: Shared deploy profiles and Edge admission

**Files:**
- Modify: `ops/stage0/test_deploy_via_ssm_bluegreen.py`
- Modify: `ops/stage0/deploy_via_ssm_bluegreen.sh`

**Interfaces:**
- Consumes: `INSTANCE_ID` with `i-*` or `mi-*`, current `/var/lib/tokenkey/.env`, active app container.
- Produces: remote `DEPLOY_PROFILE=prod|edge`; `ensure_profile_environment`; `admit_edge_candidate`.

- [x] **Step 1: Write failing render tests**

Add tests proving `mi-*` renders successfully with `DEPLOY_PROFILE=edge`, does not deliver prod QA/archive/media defaults, and includes both exact memory and disk admission formulas. Retain prod assertions under `DEPLOY_PROFILE=prod`; reject every other instance-ID shape.

- [x] **Step 2: Run the focused tests and observe failure**

Run: `python3 -m unittest ops.stage0.test_deploy_via_ssm_bluegreen.BlueGreenRenderTest`

Expected: FAIL because `mi-*` is rejected and no Edge profile/admission exists.

- [x] **Step 3: Implement the thin profiles and admission gate**

Derive `DEPLOY_PROFILE` before rendering. Deliver it to the remote script. Rename the prod-only environment initializer to a profile owner that always validates `.env`, applies existing common-safe defaults, applies prod-only injected values only for prod, and preserves Edge host values. Before any Edge candidate is started, resolve active app working set with `docker stats --no-stream`, read `/proc/meminfo` `MemAvailable`, read `df -B1 /`, log audit-only swap/load/OOM facts, and fail unless:

```text
mem_available_bytes >= max(335544320, active_working_set_bytes + 134217728)
disk_available_bytes >= 5368709120
```

- [x] **Step 4: Run render tests**

Run: `python3 -m unittest ops.stage0.test_deploy_via_ssm_bluegreen.BlueGreenRenderTest`

Expected: PASS.

### Task 2: One-cutover state machine and deterministic cleanup

**Files:**
- Modify: `ops/stage0/test_deploy_via_ssm_bluegreen.py`
- Modify: `ops/stage0/deploy_via_ssm_bluegreen.sh`

**Interfaces:**
- Consumes: healthy requested-tag candidate and the current Caddy route.
- Produces: `cleanup_on_exit`, `commit_cutover`, `observe_routed_health`; one-cutover legacy initialization.

- [x] **Step 1: Replace old-behavior tests with failing safety tests**

Assert all of the following in executable helper tests or rendered-contract assertions:

```text
EXIT trap owns cleanup; ERR trap is absent
pre_cutover cleanup removes TARGET_CONTAINER and restores ENV_BACKUP
committed cleanup preserves both colors and does not restore .env
target_is_reusable is absent and inactive color is always force-recreated
legacy starts requested tag directly as blue and cuts over once
commit_cutover performs Caddy switch, active-color write, and timestamp write before setting committed
commit failure restores the old Caddyfile and leaves CUTOVER_COMMITTED=0
both legacy and regular paths call observe_routed_health before drain/stop
observation checks the health path through tokenkey-caddy for 30 seconds
```

- [x] **Step 2: Run focused tests and observe failure**

Run: `python3 -m unittest ops.stage0.test_deploy_via_ssm_bluegreen`

Expected: FAIL on the old ERR-only cleanup, reusable candidate, two-cutover legacy path, and missing routed observation.

- [x] **Step 3: Implement the minimal state machine**

Use `trap cleanup_on_exit EXIT`. Cleanup captures bounded logs and removes only the failed target while `CUTOVER_COMMITTED=0`, then restores `.env`. Delete candidate reuse. Refactor Caddy rendering so `commit_cutover` is the only owner of backup, validation, live replacement, reload, active-color persistence, timestamp persistence, and rollback of Caddy on any incomplete commit. Set `CUTOVER_COMMITTED=1` only after all commit writes succeed.

Legacy initialization derives the repository from `tokenkey`, writes requested `${repo}:${TAG}` into blue, starts and validates blue while legacy serves, commits once, observes the live route, installs the unit, then drains/stops legacy. Regular deployment follows the same commit/observe/drain sequence. `observe_routed_health` performs successful checks through `tokenkey-caddy` for 30 seconds; a failure returns non-zero after commit so both colors remain running.

- [x] **Step 4: Run the full primitive tests**

Run: `python3 -m unittest ops.stage0.test_deploy_via_ssm_bluegreen`

Expected: PASS.

### Task 3: Edge workflow and mechanical SSOT guard

**Files:**
- Modify: `.github/workflows/deploy-edge-lightsail-stage0.yml`
- Modify: `scripts/preflight.sh`
- Modify: `ops/stage0/test_deploy_stage0_workflow.py`

**Interfaces:**
- Consumes: the shared primitive from Tasks 1–2.
- Produces: both prod and Edge workflows calling the same file; preflight rejection of the legacy Edge primitive.

- [x] **Step 1: Add failing workflow contract assertions**

Update the workflow tests to require `ops/stage0/deploy_via_ssm_bluegreen.sh` for upgrade and rollback, and forbid `ops/stage0/deploy_via_ssm.sh` in both Stage0 workflows.

- [x] **Step 2: Run focused workflow tests and observe failure**

Run: `python3 -m unittest ops.stage0.test_deploy_stage0_workflow`

Expected: FAIL because Edge still calls the legacy primitive.

- [x] **Step 3: Wire Edge and update preflight sentinel**

Change only the Edge workflow command path. Update the `Stage0 deployment primitive sharing` sentinel and messages to require the shared blue/green owner in both workflows and reject the legacy path.

- [x] **Step 4: Run workflow tests**

Run: `python3 -m unittest ops.stage0.test_deploy_stage0_workflow`

Expected: PASS.

### Task 4: Fleet-aware Edge canary facts and selection

**Files:**
- Create: `ops/stage0/edge_release_canary_probe.sh`
- Create: `ops/stage0/test_edge_release_canary_probe.py`
- Modify: `scripts/stage0/pick_release_canary_edge.py`
- Modify: `scripts/stage0/test_pick_release_canary_edge.py`

**Interfaces:**
- Consumes: `ops/lib/resolve-app-container.sh`, `/proc/meminfo`, `docker stats`, `df`, 30-minute app logs, and `edge_oauth_pool_probe.sh` eligibility output.
- Produces: one strict JSON object with integer/null facts, `eligible`, and ordered `rejection_reasons`; selector returns `(canary_edge, complete_audit)`.

- [x] **Step 1: Write failing probe and selector tests**

Cover strict JSON parsing; both capacity thresholds; missing/invalid required facts; OAuth count remaining audit-only; probing every Edge despite earlier eligibility; transport failure rejecting one Edge; all OAuth pools empty still selecting; sorting by traffic, memory headroom, and matrix order; no eligible Edge returning `None`.

- [x] **Step 2: Run focused tests and observe failure**

Run: `python3 -m unittest ops.stage0.test_edge_release_canary_probe scripts.stage0.test_pick_release_canary_edge`

Expected: FAIL because the probe is missing and the selector still returns on the first positive OAuth count.

- [x] **Step 3: Implement the read-only probe**

Emit one JSON line containing:

```json
{"mem_available_bytes":0,"active_app_working_set_bytes":0,"memory_required_bytes":335544320,"memory_headroom_bytes":0,"disk_available_bytes":0,"completed_requests_30m":0,"oauth_account_count":0,"eligible":false,"rejection_reasons":[]}
```

Use the canonical app resolver, the existing `http request completed` log marker, and call the existing OAuth pool probe rather than copying its SQL. Missing required capacity/traffic values must be JSON `null`, mark the host ineligible, and name the reason.

- [x] **Step 4: Implement full-fleet selection**

Call the new probe through `run-probe.sh` once for every deployable Edge, parse the final JSON line strictly, retain every row, filter `eligible=true`, then sort by:

```python
(completed_requests_30m, -memory_headroom_bytes, matrix_index)
```

Keep CLI compatibility for plain edge ID and `--json`, but update help/error text to capacity/traffic semantics.

- [x] **Step 5: Run focused tests**

Run: `python3 -m unittest ops.stage0.test_edge_release_canary_probe scripts.stage0.test_pick_release_canary_edge`

Expected: PASS.

### Task 5: Documentation contracts and complete verification

**Files:**
- Modify if required: `.cursor/skills/tokenkey-stage0-release-rollout/SKILL.md`
- Modify: tests/sentinels discovered by preflight failures only when they encode the replaced OAuth-first or Edge single-app contract.

**Interfaces:**
- Consumes: all implementation tasks.
- Produces: no stale operator instruction and a green repository gate.

- [x] **Step 1: Search for stale contracts**

Run:

```bash
grep -RInE 'OAuth-first|first.*OAuth|single-app.*Edge|Edge.*single-app|deploy_via_ssm\.sh' .cursor/skills docs/approved ops scripts .github/workflows --exclude-dir=.git
```

Update only operator-facing or mechanical references contradicted by the approved spec. If the rollout skill changes, follow `writing-skills` and keep `.cursor/skills` as the only edited skill source.

- [x] **Step 2: Run all focused release-safety tests**

Run:

```bash
python3 -m unittest \
  ops.stage0.test_deploy_via_ssm_bluegreen \
  ops.stage0.test_deploy_stage0_workflow \
  ops.stage0.test_edge_release_canary_probe \
  scripts.stage0.test_pick_release_canary_edge \
  scripts.stage0.test_rollout_edges \
  scripts.stage0.test_dispatch_edge_deploy_smoke_phase
```

Expected: PASS.

- [x] **Step 3: Run shell syntax checks**

Run:

```bash
bash -n ops/stage0/deploy_via_ssm_bluegreen.sh
bash -n ops/stage0/edge_release_canary_probe.sh
```

Expected: PASS.

- [x] **Step 4: Run the complete mechanical gate**

Run: `./scripts/preflight.sh`

Expected: PASS with the Stage0 primitive sentinel confirming prod and Edge share blue/green.

- [x] **Step 5: Review the final diff against the approved spec**

Run: `git diff --check && git diff --stat && git status --short`

Confirm every approved behavior has one code owner, no unrelated files changed, and no deployment/release was triggered.

### Task 6: Shared target-tag migration gate

**Files:**
- Modify: `scripts/checks/bluegreen-migration-safety.py`
- Modify: `.github/workflows/deploy-stage0.yml`
- Modify: `.github/workflows/deploy-edge-lightsail-stage0.yml`
- Modify: `ops/stage0/test_deploy_stage0_workflow.py`
- Modify: `scripts/preflight.sh`

**Interfaces:**
- Consumes: requested release tag without leading `v`, git remote/tag metadata, changed SQL migrations.
- Produces: one fail-closed migration verdict used before prod deploy or Edge upgrade/rollback mutation.

- [ ] **Step 1: Write failing checker and workflow contract tests**

Extend the checker's existing `--selftest` with exact target-tag resolution,
previous stable release selection, unresolved target/range failure, destructive
SQL detection, and the existing explicit acknowledgement. Require both
workflows to call only:

```text
python3 scripts/checks/bluegreen-migration-safety.py --target-tag "$INPUT_TAG"
```

Forbid workflow-owned `git ls-remote`, previous-tag Python snippets, or direct
`--base/--head` deploy logic.

- [ ] **Step 2: Run focused tests and observe failure**

Run:

```bash
python3 -m unittest ops.stage0.test_deploy_stage0_workflow
python3 scripts/checks/bluegreen-migration-safety.py --selftest
```

Expected: FAIL because the checker lacks `--target-tag` and Edge has no
migration gate.

- [ ] **Step 3: Move release-range ownership into the checker**

Implement `--target-tag` as the deploy entry point. Normalize to `vX.Y.Z`,
fetch/verify the requested tag, select and fetch the greatest lower stable tag,
and scan that exact range. Preserve `--base/--head` for local preflight only.
Deploy-mode resolution errors return non-zero; no `origin/main..HEAD` fallback
is allowed for an unresolved requested release.

- [ ] **Step 4: Replace both workflow implementations with thin calls**

Run the shared checker after tag/manifest validation and before AWS credentials
or app mutation. The Edge gate applies to `upgrade` and `rollback`; initial
`provision` has no old application sharing its database and remains outside the
blue/green compatibility gate.

- [ ] **Step 5: Add the migration-owner sentinel and rerun tests**

Extend preflight to require both workflow calls and reject duplicated
range-selection logic. Rerun the Step 2 commands plus:

```bash
./scripts/preflight.sh
```

Expected: PASS.

### Task 7: Fail-closed exceptional cutover recovery

**Files:**
- Modify: `ops/stage0/test_deploy_via_ssm_bluegreen.py`
- Modify: `ops/stage0/deploy_via_ssm_bluegreen.sh`

**Interfaces:**
- Consumes: failed target-route preservation/reload and existing Caddy plus `active-color` state.
- Produces: honest durable recovery state, restart-safe dual-color service, and a blocked next deploy.

- [ ] **Step 1: Write failing recovery tests**

Prove:

```text
confirmed target reload may persist target active-color
unconfirmed target reload removes active-color and preserves both colors
systemd starts both colors when active-color is missing or inconsistent
colored Caddy route + missing active-color is not legacy and blocks deploy
cleanup never removes either preserved color in this state
```

- [ ] **Step 2: Run focused tests and observe failure**

Run: `python3 -m unittest ops.stage0.test_deploy_via_ssm_bluegreen`

Expected: FAIL because `preserve_target_cutover` currently ignores reload
failure and still writes target `active-color`.

- [ ] **Step 3: Implement the minimal recovery semantics**

Make `preserve_target_cutover` distinguish confirmed from unconfirmed target
reload. On unconfirmed reload, remove `active-color`, retain both colors, and
return failure without entering candidate cleanup. Tighten
`ensure_legacy_cutover` so a colored route without `active-color` fails with an
explicit recovery diagnostic. Reuse the existing systemd disagreement path;
do not add a marker file or third deploy state.

- [ ] **Step 4: Run focused and shell checks**

Run:

```bash
python3 -m unittest ops.stage0.test_deploy_via_ssm_bluegreen
bash -n ops/stage0/deploy_via_ssm_bluegreen.sh
```

Expected: PASS.

### Task 8: Capacity policy data SSOT

**Files:**
- Create: `ops/stage0/bluegreen-capacity-policy.env`
- Modify: `ops/stage0/deploy_via_ssm_bluegreen.sh`
- Modify: `ops/stage0/edge_release_canary_probe.sh`
- Modify: `scripts/stage0/pick_release_canary_edge.py`
- Modify: `ops/stage0/test_deploy_via_ssm_bluegreen.py`
- Modify: `ops/stage0/test_edge_release_canary_probe.py`
- Modify: `scripts/stage0/test_pick_release_canary_edge.py`
- Modify: `scripts/preflight.sh`

**Interfaces:**
- Consumes: exactly three `KEY=decimal-bytes` fields.
- Produces: identical memory floor, active-app headroom, and root-disk floor for deploy, probe, and picker validation.

- [ ] **Step 1: Write failing policy-consumer tests**

Require all three consumers to reject missing, duplicate, unknown, or
non-decimal policy fields. Assert the policy file is included in remote probe
transport and that the three byte literals do not occur in consumer code.

- [ ] **Step 2: Run focused tests and observe failure**

Run:

```bash
python3 -m unittest \
  ops.stage0.test_deploy_via_ssm_bluegreen \
  ops.stage0.test_edge_release_canary_probe \
  scripts.stage0.test_pick_release_canary_edge
```

Expected: FAIL because each consumer currently owns part of the policy.

- [ ] **Step 3: Add and consume the data-only policy**

Create the file with only:

```text
EDGE_MIN_MEM_AVAILABLE_BYTES=335544320
EDGE_ACTIVE_APP_HEADROOM_BYTES=134217728
EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES=5368709120
```

Shell consumers validate and source it; the deploy primitive renders the
validated values into the remote script. `run-probe.sh` uploads the same file
for the Edge probe. The picker strictly parses the local file and uses those
values to validate returned facts. Do not create a general release-policy
framework.

- [ ] **Step 4: Add the capacity-owner sentinel and rerun tests**

Preflight requires the file and its three consumers, and rejects the three byte
literals outside the policy file and tests/docs. Run the Step 2 command plus:

```bash
bash -n ops/stage0/deploy_via_ssm_bluegreen.sh
bash -n ops/stage0/edge_release_canary_probe.sh
```

Expected: PASS.

### Task 9: Current operational docs and complete SSOT verification

**Files:**
- Modify: `deploy/aws/RUNBOOK-disaster-recovery.md`
- Modify: `deploy/aws/lightsail/README.md`
- Modify: `deploy/aws/README.md`
- Modify: `docs/spec-delta/edge-lightsail.md`
- Modify: `docs/deploy/blue-green-zero-downtime-backlog.md`
- Modify: `scripts/preflight.sh`
- Modify: focused tests/sentinels only where needed.

**Interfaces:**
- Consumes: the shared migration, capacity, deploy, recovery, and canary owners.
- Produces: current operator guidance with no competing active deployment contract.

- [ ] **Step 1: Add a failing current-doc contract check**

Enumerate current operational docs explicitly. Reject statements that describe
`deploy_via_ssm.sh` or stop-first single-container replacement as the current
prod/Edge upgrade or rollback path. Do not scan archive directories as current
instructions.

- [ ] **Step 2: Align current docs and mark history**

Point current runbooks and READMEs to
`ops/stage0/deploy_via_ssm_bluegreen.sh` and this approved spec. Correct the
first-run sequence to requested-tag blue candidate → one cutover → legacy
drain. Mark the zero-downtime backlog and any retained design-delta claims that
describe the old Edge path as superseded; preserve their historical context.

- [ ] **Step 3: Run focused release-safety verification**

Run:

```bash
python3 -m unittest \
  ops.stage0.test_deploy_via_ssm_bluegreen \
  ops.stage0.test_deploy_stage0_workflow \
  ops.stage0.test_edge_release_canary_probe \
  scripts.stage0.test_pick_release_canary_edge \
  scripts.stage0.test_rollout_edges \
  scripts.stage0.test_dispatch_edge_deploy_smoke_phase
bash -n ops/stage0/deploy_via_ssm_bluegreen.sh
bash -n ops/stage0/edge_release_canary_probe.sh
git diff --check
```

Expected: PASS.

- [ ] **Step 4: Run the complete mechanical gate**

Run: `./scripts/preflight.sh`

Expected: PASS with distinct confirmations for the shared migration gate,
capacity-policy uniqueness, blue/green primitive sharing, and current-doc
contract.

- [ ] **Step 5: Final SSOT review**

Search the entire non-archive repository for duplicated migration-range logic,
capacity literals, current legacy deployment claims, and independent cutover
state. Confirm every load-bearing behavior points to exactly one executable
owner and one canonical approved spec. Do not deploy, release, probe upstream
accounts, push, or merge during this task.
