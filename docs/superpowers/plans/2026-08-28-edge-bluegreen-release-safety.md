# Edge Blue/Green Release Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make prod and Lightsail Edge share one fail-safe same-host blue/green deploy primitive and select the Edge canary from measured capacity and recent traffic.

**Architecture:** `ops/stage0/deploy_via_ssm_bluegreen.sh` remains the sole deploy state machine; the managed-instance ID selects only a prod or Edge environment profile. A new read-only Edge release probe emits one strict JSON record per host, and `scripts/stage0/pick_oauth_canary_edge.py` probes the full fleet before applying the approved deterministic sort.

**Tech Stack:** Bash, AWS SSM, Docker Compose, Caddy, Python `unittest`, GitHub Actions YAML.

**Spec:** `docs/approved/deploy-stage0-workflow.md`

## Global Constraints

- Externally meaningful deploy state is only `pre_cutover` or `committed`.
- A pre-cutover failure removes the failed candidate and restores the saved `.env`; a committed failure never automatically switches Caddy back.
- Edge admission requires `MemAvailable >= max(320 MiB, active app working set + 128 MiB)` and at least 5 GiB free on `/`.
- Candidate `/health` owns protocol readiness; deployment code must not infer or probe protocol support.
- Steady state runs one app color; post-commit routed `/health` must pass for 30 seconds before stopping the previous app.
- Canary ordering is lowest completed requests over 30 minutes, greatest `MemAvailable` headroom, then matrix order; OAuth/Kiro count is audit-only.
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
- Modify: `scripts/stage0/pick_oauth_canary_edge.py`
- Modify: `scripts/stage0/test_pick_oauth_canary_edge.py`

**Interfaces:**
- Consumes: `ops/lib/resolve-app-container.sh`, `/proc/meminfo`, `docker stats`, `df`, 30-minute app logs, and `edge_oauth_pool_probe.sh` eligibility output.
- Produces: one strict JSON object with integer/null facts, `eligible`, and ordered `rejection_reasons`; selector returns `(canary_edge, complete_audit)`.

- [x] **Step 1: Write failing probe and selector tests**

Cover strict JSON parsing; both capacity thresholds; missing/invalid required facts; OAuth count remaining audit-only; probing every Edge despite earlier eligibility; transport failure rejecting one Edge; all OAuth pools empty still selecting; sorting by traffic, memory headroom, and matrix order; no eligible Edge returning `None`.

- [x] **Step 2: Run focused tests and observe failure**

Run: `python3 -m unittest ops.stage0.test_edge_release_canary_probe scripts.stage0.test_pick_oauth_canary_edge`

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

Run: `python3 -m unittest ops.stage0.test_edge_release_canary_probe scripts.stage0.test_pick_oauth_canary_edge`

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
  scripts.stage0.test_pick_oauth_canary_edge \
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
