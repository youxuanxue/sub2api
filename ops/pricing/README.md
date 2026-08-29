# TokenKey model operations

交付判定、owner 与 precedence 只在
`docs/approved/pricing-serving-single-source-of-truth.md` 维护。本文只说明运维工具：probe、traffic、
upstream discovery 和 availability 都是证据；不得据此单独声明当前请求可交付。

The public `/pricing` catalog and the per-user "Your Menu" share one catalog projection:
`supportedCatalogModelIDsForPlatform` feeds the menu fallback, and
`FilterPublicCatalogToServable` filters the public catalog from the same empirical sets.
This projection does not claim request-time reachability. The sets live in
`backend/internal/service/pricing_catalog_supported_models_tk.go` between
`servable-allowlist:begin/end <platform>` markers. The refresh tool rewrites the
anthropic/openai/gemini blocks from live probes; antigravity and grok are still
hand-maintained empirical sets in the same file.

## Files

| File | Role |
| --- | --- |
| `probe-servable-models.sh` | Runs on prod or an edge via `ops/observability/run-probe.sh`. Sends one minimal real request per candidate model and emits `platform⇥model⇥http⇥verdict` TSV. A model is **servable** iff it returns a real `200`. Always auto-ensures reusable `__tk_probe_<scope>_group` / `__tk_probe_<scope>_key` per platform via `probe_reserved_resources.sh` (no direct-key fallback, no dependency on `TK_SMOKE_API_KEY` or customer keys). The companion is mandatory — deliver it with `run-probe.sh --with ops/pricing/probe_reserved_resources.sh` (the orchestrator and every manual invocation below do). |
| `probe-antigravity-gemini25pro-literal.sh` | Focused prod probe for literal Antigravity chat ids on the `Google-Gemini` source group (default: `gemini-pro-agent`, `gemini-2.5-pro`). Hits both `/v1/chat/completions` and `/antigravity/v1beta/models/{id}:generateContent`, emits TSV plus a non-secret account snapshot. Companion: `probe_reserved_resources.sh`. Used when the broad servable refresh batch cannot distinguish generateContent timeout vs routing gaps for `gemini-2.5-pro`. |
| `probe-volcengine-agent-plan-models.sh` | Direct Ark **Agent Plan** activation probe (`/api/plan/v3/*`, not pay-as-you-go `/api/v3`). Reads prod account `AGENT_PLAN_ACCOUNT_ID` (default 88) credentials and classifies chat/responses candidates as servable/unsupported. Used before wiring Agent Plan `model_mapping` / manifest SSOT. |
| `probe_reserved_resources.sh` | Shared DB helpers for reserved probe groups/keys (same namespace as `tokenkey-account-model-probe`). Per-scope `flock` on `/tmp/tokenkey-account-model-probe-<scope>.lock` serializes `account_groups` mutations vs account-model probes. Catalog refresh copies schedulable accounts from canonical source group ids by default, probes, then clears `account_groups` bindings and releases locks on EXIT. Group-name overrides are legacy diagnostics only. |
| `ops/stage0/probe_direct_upstream_model.sh` | Dispatcher for provider-direct account probes. It bypasses TokenKey gateway/catalog/model_mapping floor and calls only implemented direct scripts (`openai`, `grok`); unsupported platforms return `setup_error` rather than falling back to gateway. |
| `ops/stage0/probe_openai_upstream_model.sh` | Direct ChatGPT Codex upstream probe for one OpenAI OAuth account. Posts to `chatgpt.com/backend-api/codex/responses` with Codex headers and emits JSON `verdict`. Use when gateway/account probes may be blocked by TokenKey floor. Grok direct probing is `ops/stage0/probe_grok_upstream_model.sh`. |
| `probe-traffic-proven-models.sh` | Runs on prod via `ops/observability/run-probe.sh`. Read-only over `usage_logs`: emits `platform⇥model⇥hits` for every model that served **real successful traffic** in the last `TRAFFIC_HOURS` (default 24). Feeds the `--skip-proven-by-traffic` short-circuit below. Positive evidence only — a model with no recent traffic is simply absent (never an unsupported signal). |
| `refresh-servable-allowlist.py` | Refreshes the shared public-catalog/user-menu servable sets. It derives candidates, runs probes (uploads `probe_reserved_resources.sh` via `run-probe.sh --with`), keeps `verdict==servable`, de-duplicates dated snapshots, and splices the anthropic/openai/gemini Go blocks. `selftest` covers deterministic glue (no prod). Optional `--skip-proven-by-traffic` short-circuits candidates already proven by 24h traffic out of the probe batches. |
| `modelops.py` | Model-operations entry. `plan` is read-only; `activate` validates current/target bundles plus independent probe/pricing evidence, renders the prod mapping plan by default, and writes only with the fixed confirmation phrase. |
| `reconcile-served-models.py` | Compatibility wrapper for `modelops.py`. New runbooks should call `modelops.py`. |
| `model-surface-bundle.json` | Deterministic, checksummed release projection generated once from the Go model owner and published as a release asset. |
| `model_surface_bundle.py` | Shared stdlib-only schema and digest validator used by rollout tools; it owns no model list. |
| `model-surface-refresh-report.py` | Offline/read-only evidence normalizer. It consumes saved account inventory, authoritative candidate discovery, direct-upstream/gateway/traffic evidence, the bundle, pricing overlay, and reprobe ledger; emits per-shared-scope deltas plus the Vertex ch41 intersection/union/profile clusters. It never probes or writes. |
| `manage-account-model-mapping-runtime.py` | Consumes a generated bundle without compiling Go, hot-pushes optional runtime replacement scopes, checks required floor + forbidden policy while preserving compatible extras, keeps Edge diagnostics available, and applies reviewed account/group diffs only through explicit confirmation. |
| `pricing-registry-sensor.py` | Compares provider/LiteLLM evidence with the complete registry and prepares a registry-only draft PR candidate. It never publishes or changes serving. |
| `manage-overlay-runtime.py` | Publishes or audits the exact protected-main registry envelope. `sync-runtime` is reserved for the protected publisher workflow; operators use `check` for read-only drift inspection. |
| `apply-pricing-hotfix.py` | Legacy helper for provider lookup and explicitly scoped `channel_model_pricing` operations. Its output is evidence only for global prices; durable global changes go through the registry PR and protected publisher. |

## Re-run (operator, needs AWS creds for prod SSM)

```bash
# 0. preview the candidate split (no prod)
python3 ops/pricing/refresh-servable-allowlist.py candidates

# 1. probe + rewrite the Go allowlist blocks in one shot
python3 ops/pricing/refresh-servable-allowlist.py run

# 2. review the Go diff, then open a PR (or pass --open-pr to step 1)
cd backend && go test -tags=unit ./internal/service/ -run PublicCatalog
git diff backend/internal/service/pricing_catalog_supported_models_tk.go
```

Split the steps when you want to inspect the raw verdicts first:

```bash
python3 ops/pricing/refresh-servable-allowlist.py probe | tee /tmp/servable.tsv
python3 ops/pricing/refresh-servable-allowlist.py apply --results /tmp/servable.tsv
```

### 24h-traffic short-circuit (`--skip-proven-by-traffic`)

A full probe is ~160 models in ~16 SSM batches (8–15 min). Many of those models
already served **real successful traffic** in the last day — re-probing them is
pure latency. `--skip-proven-by-traffic` (or env `REFRESH_SKIP_PROVEN_BY_TRAFFIC=1`)
queries `usage_logs` once up front and skips the probe for any candidate already
proven servable, cutting the batch count:

```bash
python3 ops/pricing/refresh-servable-allowlist.py run --skip-proven-by-traffic
# tune the window (default 24h):
python3 ops/pricing/refresh-servable-allowlist.py run --skip-proven-by-traffic --traffic-hours 48
```

It logs exactly what it skipped, for human review:

```
[refresh] skipping 37 models proven by 24h traffic: anthropic/claude-opus-4-8, openai/gpt-5.4, …
[refresh] probing 41 models in 5 batch(es) of <= 12 …
```

**Why it is safe (purely additive — read the contract before changing it):**

- **Traffic success = servable** is firm positive evidence; **no traffic ≠
  unsupported**. A candidate that did not show up in the window is *still probed*
  normally — the short-circuit only ever removes probes, never marks anything
  unsupported. Default is **off** so the conservative full probe stays the baseline;
  enable it as a graduated rollout.
- **Only candidates can be skipped/added.** The proven set is intersected with the
  derived candidate set, so traffic can never inject a model that is not already a
  priced/known candidate.
- **Platform bucket comes from the candidate set, not the serving account.** Vertex
  is served under `accounts.platform='newapi'`, so a served `gemini-2.5-pro` is
  bucketed as `gemini` because that is its *candidate* platform. The script's
  `usage_logs` row only needs the model id to match.
- **Blocked models cannot revive.** skiplist/deadlist entries are already absent
  from the candidate set, and the proven set is additionally re-checked against the
  reprobe ledger (`validate_results_against_reprobe_ledger`) — one successful
  request cannot bring a deadlisted model back.
- A `usage_logs` row is a **metered** request (errors that burn no tokens are not
  logged); the query additionally requires real generation (tokens / image / video)
  so a `$0` placeholder row never counts as proof.

## Offline model-surface refresh report

Keep discovery and probing in the existing platform-specific tools. Once their
outputs are saved, normalize them without network or runtime writes:

```bash
python3 ops/pricing/model-surface-refresh-report.py generate \
  --inventory /tmp/model-account-inventory.json \
  --candidates /tmp/model-candidates.json \
  --raw-probe /tmp/raw-provider-results.json \
  --gateway-probe /tmp/gateway-results.json \
  --traffic-evidence /tmp/traffic.tsv \
  --format text

python3 ops/pricing/model-surface-refresh-report.py --selftest
```

Candidate JSON should carry `observed_at`, `authoritative: true`, and a `scopes`
object. JSON probe rows should carry `observed_at`; a promotion requires direct and
gateway success for the same active account, and the gateway row additionally needs
matching `account_id` and `usage_account_id`. Legacy TSV inputs cannot prove that
same-account join and therefore remain classification-only; their file mtime is used
as the evidence time and expires after 24 hours. Missing/stale
candidate authority, non-ch41 account divergence, inconclusive 401/403/429/5xx, local
mapping-floor rejection, missing price, and unapproved paid-media evidence all
block `proposed_add` rather than inferring support.

## Modelops plan and activation

**Operator entry:** skill `tokenkey-modelops-planner` (`.cursor/skills/tokenkey-modelops-planner/SKILL.md`).
Script implementation below; do not treat this README as the primary runbook.

For newapi long-tail, live runtime mapping checks, and mirror-account
operations, use the planner to turn discovery/probe/pricing/runtime facts into
a reviewable plan:

```bash
# Generate read-only SQL for a live model_mapping snapshot, then run it through
# the normal prod DB access path and save the JSON result locally. For volatile
# pools such as Qwen/DashScope, snapshot by channel_type instead of copying
# account ids from old docs.
python3 ops/pricing/modelops.py snapshot-sql --channel-type 17

# Compare upstream discovery, probe results, live mapping, and any explicitly
# reviewed mirror pair from that same runtime snapshot.
python3 ops/pricing/modelops.py plan \
  --upstream "$QWEN_ACCOUNT_ID":/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/qwen_mapping_snapshot.json \
  --mirror "$SOURCE_QWEN_ACCOUNT_ID":"$TARGET_QWEN_ACCOUNT_ID"
```

The planner's output is intentionally an operator plan, not an apply loop:
`probe_needed` includes grouped `run-probe.sh` commands, `price_missing` points
to `apply-pricing-hotfix.py lookup`, `mapping_missing` prints guarded
`apply-model-mapping-live.py --dry-run` commands, and `mirror_drift` reports
exact key/value differences. It also names the shared catalog/menu surface so
operators do not hand-maintain a second menu list. Apply still goes through
migrations, the guarded live model-mapping tool, `refresh-servable-allowlist.py`,
or pricing-hotfix after review.

`modelops activate` is a separate prod-only path. It compares the current and
target release bundles, requires fresh independent `servable` and `priced`
evidence for every added/retargeted required mapping, then invokes the mapping
manager dry-run and read-only release gate. Evidence fields and the fixed
24-hour freshness rule are defined in
`docs/approved/model-surface-activation-contract.md`. Add
`--confirm yes-activate-model-surface` only after reviewing the default plan.
Activation rejects a live `tk_account_model_mapping_runtime` setting because it
would shadow the artifact covered by evidence; fold it into the bundle or clear
it first. It pins one resolved prod instance for plan/apply/post-gate and repeats
the runtime-absence check under a database lock before account writes. Direct
runtime and Edge maintenance commands remain available.

## Account model_mapping runtime hot update

Operator entry: skill `tokenkey-modelops-planner`, branch D. The runtime JSON is
a scope replacement layer: each listed platform or newapi `channel_type`
replaces the compiled account mapping floor for that scope; omitted scopes keep
the compiled floor.

**prod-only model-surface alignment check.** 公开目录、商业价格与 prod mapping intent 必须
一致，但它们不是三套 request gate：catalog allowlists、active registry/scoped channel rows 与 prod
`accounts.credentials.model_mapping` (plus optional runtime replacement). When a
bundle contains `account_overrides`, the property-based
`account_override:<platform>:<channel_type>:<base_url>` scope overrides the
shared platform/channel floor only when all selector metadata matches; account IDs
are not used as override selectors. Vertex `newapi/channel_type=41` is the one
capability-profile exception: public display is the verified union, the shared
channel floor is the strict successful intersection, and a normalized
`vertex_capability_profile` selects a complete named profile floor. Missing or
unknown profiles use the shared floor and produce a check violation; no other
platform/channel has account-level profiles.
Post-release diagnostics use `check-accounts` **without** `--include-edges`;
its expected mappings and forbidden policy metadata come from the selected
checksummed bundle.
Violations are yellow configuration drift and do not change a successful deploy,
smoke, or rollback verdict.

`release-gate` is reserved for an explicit modelops/model-activation precheck.
It verifies that live prod covers the selected bundle's release floor while
allowing preheated extras, except keys/prefixes explicitly forbidden by that Go
SSOT. It does not run in generic `deploy-stage0.yml`.

**Official upstream aliases:** when a managed platform or newapi channel's provider
model page declares an id/alias and TokenKey has a canonical price owner plus reviewed
probe evidence, it may enter the public catalog. This is CatalogPolicy activation evidence,
not a substitute for request-time Plan or runtime capacity. Retirement redirects without a
current official SKU stay priced-only. Canonical wording:
`docs/global/agent-reference.md` § Model serving SSOT.

**Edge empty mapping is expected.** Traffic is `user → prod → edge relay → upstream`.
Do not bulk-copy prod floors to edges or treat edge empty mappings as violations
in routine gates. Empty Antigravity accounts serve the compiled default plus
`tk_account_model_mapping_runtime` overlay (`sync-runtime` hot-adds models there).
Non-empty mappings stay authoritative and still need `apply-accounts`. Add
`--include-edges` only for deliberate edge troubleshooting — not for release sign-off.
Canonical wording: `docs/global/agent-reference.md` § Model serving SSOT.

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py --selftest
python3 ops/pricing/manage-account-model-mapping-runtime.py validate \
  --file /tmp/account-model-mapping-runtime.json \
  --bundle /tmp/model-surface-bundle.json
python3 ops/pricing/manage-account-model-mapping-runtime.py check --file /tmp/account-model-mapping-runtime.json

# after review, only updates settings on prod + deployable edges (does not mutate accounts):
python3 ops/pricing/manage-account-model-mapping-runtime.py sync-runtime --file /tmp/account-model-mapping-runtime.json

# post-release / post-hotfix read-only diff (prod only; add --include-edges for deployable edges):
python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json

# explicit modelops/model-activation floor precheck; never a generic deploy/rollback dependency:
python3 ops/pricing/manage-account-model-mapping-runtime.py release-gate

# ch41 only: verify id/name/platform/channel/current-profile, then assign the
# profile property on prod without changing model_mapping in the same operation:
python3 ops/pricing/manage-account-model-mapping-runtime.py assign-vertex-profiles \
  --file /tmp/vertex-profile-assignments.json \
  --bundle /tmp/model-surface-bundle.json \
  --dry-run
python3 ops/pricing/manage-account-model-mapping-runtime.py assign-vertex-profiles \
  --file /tmp/vertex-profile-assignments.json \
  --bundle /tmp/model-surface-bundle.json \
  --confirm yes-assign-vertex-capability-profiles

# after reviewing the separate mapping diff, explicitly apply account/group changes:
python3 ops/pricing/manage-account-model-mapping-runtime.py apply-accounts \
  --target prod \
  --bundle /tmp/model-surface-bundle.json \
  --confirm yes-apply-account-model-mapping

# model activation defaults to evidence validation + prod dry-run; no writes:
python3 ops/pricing/modelops.py activate \
  --bundle /tmp/model-surface-bundle.json \
  --current-bundle /tmp/current-model-surface-bundle.json \
  --probe-evidence /tmp/model-activation-probe.json \
  --pricing-evidence /tmp/model-activation-pricing.json
```

For a newly served model, preheat the live materialization first and run the
explicit modelops `release-gate` before activating the tag whose Go floor exposes
it. That gate may block the model activation, not the generic deployment
workflow. `deploy-stage0.yml` and rollback remain independent of live mapping
convergence and target-tag helper availability; they are accepted by image,
health, smoke, and display-canary results. Post-release `check-accounts` reports
any remaining drift as yellow.

## Pricing-missing remediation (Feishu「模型缺价」)

The Feishu card identifies the requested, routed, and billing model relationship.
First verify the owner and every billable dimension against an official source;
provider/LiteLLM output is comparison evidence, never effective billing input.

For a global price, edit only
`backend/internal/service/tk_pricing_overlay.json`, run the registry gate, and open
a PR. A merge to protected `main` triggers `pricing-registry-publish.yml`, which
publishes the exact merged bytes as one atomic runtime snapshot without an
application release:

```bash
python3 ops/pricing/pricing-registry-sensor.py \
  --report-json /tmp/pricing-registry-sensor.json \
  --report-md /tmp/pricing-registry-sensor.md
# review official evidence, then edit the one registry owner
python3 scripts/checks/pricing-overlay.py
python3 scripts/checks/pricing-registry-publication.py
```

Use `channel_model_pricing` only when the price is intentionally scoped to one
channel. It wins within that scope but is not a global hotfix owner. Do not call
`manage-overlay-runtime.py sync-runtime` from a workstation: publication accepts
only the exact current `origin/main` registry through the protected-main
publisher. New workflow runs supersede obsolete jobs, while PostgreSQL
compare-and-set prevents any already-detached stale SSM command from overwriting
a newer snapshot. Deploy and sensor paths remain non-publishing. Rollback is a
Git revert of the registry followed by the same publisher. Alert digest cadence remains
`feishu.pricing_missing_digest_seconds` (default 1800s).

## Classification & de-dup rules

- `200` → **servable** (kept). `400/404 + retired/not-found/"not supported when
  using Codex"` → unsupported. `429/502/503` → inconclusive (capacity / wrong
  protocol / no account on the probed group). `401/403` → auth_error (probe
  setup wrong, not a model signal — fix and re-run).
- `400 Unsupported model: <id>`, `account_id=null`, empty-pool, or no upstream
  event from a prod catalog probe is not enough to answer raw provider capability.
  Prod accounts follow the SSOT account `model_mapping`; TokenKey can reject a
  new model before account selection because the current mapping/floor omits it.
  Use `ops/stage0/probe_account_model.sh` with
  `--with ops/pricing/probe_reserved_resources.sh` on a specific prod or edge
  account to prove the TokenKey path, or
  `ops/stage0/probe_direct_upstream_model.sh` with the OpenAI/Grok companion
  scripts when raw upstream capability is the authoritative truth. Only promote
  after the platform-appropriate probe returns `servable`, the gateway account
  probe confirms target-account usage, and the prod `model_mapping` path is
  updated/re-probed.
- De-dup: when both a non-dated form and its dated snapshot serve
  (`-YYYYMMDD` for anthropic, `-YYYY-MM-DD` for openai), keep only the
  non-dated; drop `-thinking` pricing pseudo-entries.
- OpenAI candidates are routed by family: `*codex*` → `/v1/responses`,
  `*image*` → `/v1/images/generations` (2026-07-02: group_id=2 returns 400
  missing `api.model.images.request` scope, so GPT image stays out until an
  image-scoped account probes 200), everything else → `/v1/chat/completions`.

## Caveats

- The probe's default source pools are group-id anchored: prod `openai=2`,
  `anthropic mirror=1`, `antigravity=21`, `Vertex/newapi=16`, `Qwen/newapi=18`,
  `GLM/newapi=26`, `VolcEngine/newapi=5`; edge-native probes use `anthropic=1`
  and `grok=4` on the target edge DB. Display names are operator-editable and
  only accepted through explicit legacy `PROBE_*_SOURCE_GROUP` overrides for
  diagnostics.
- GPT-5.6 family (#1322, 2026-07-10): `gpt-5.6` / `gpt-5.6-sol` / `gpt-5.6-terra` /
  `gpt-5.6-luna` are in `supportedOpenAICatalogModels` and
  `ops/pricing/examples/openai-oauth-proven.json` after GPT-pro1 (#9) direct
  upstream proof with Codex `0.144.1`. Bare `gpt-5.6` is a compatibility alias
  (wire transform → `gpt-5.6-sol`); keep `gpt-5.6-chat-latest` out of the
  allowlist. After code SSOT changes, sync `tk_account_model_mapping_runtime`
  and run `apply-accounts` — a stale runtime overlay will otherwise narrow
  OAuth accounts back below the compiled floor.
- Antigravity has two distinct probe surfaces: text/capability checks use
  `ANTIGRAVITY_CHAT_MODELS` on `/antigravity/v1beta`, while Studio
  gemini-native image uses `ANTIGRAVITY_IMAGE_MODELS` on `/v1/chat/completions`.
  Do not use a v1beta image 404 as a Studio image verdict.
- VolcEngine/Ark has two distinct probe surfaces: `ARK_*` calls the upstream Ark
  data plane directly and proves account activation; `VOLCENGINE_IMAGE_MODELS`
  and `VOLCENGINE_VIDEO_MODELS` call the prod TokenKey gateway through group_id
  `5` and prove end-to-end serving after pricing + `model_mapping` are live.
- The probe tests anthropic **edge-native** — rotated across the deployable edges
  (`deployable_edges()` from `edge-targets-lightsail.json`), servable if any edge serves.
  A separate warning-only pass re-probes the edge-servable set through the prod gateway
  per mirror sub-pool (`cc-*` anthropic-OAuth + `kiro-*` Kiro) and warns on
  "edge serves but prod relay does not"; those rows never enter the allowlist. Models served
  exclusively by yet another group read inconclusive here and are dropped; provide that
  group's group id and extend the probe to re-add them.
- This is a snapshot. Re-run after the served fleet changes (new model family,
  account/tier changes, an upstream sunset).

### Antigravity `gemini-2.5-pro` literal probe

The broad servable batch times out on `gemini-2.5-pro` generateContent.
Historical snapshot notes live in `docs/all-platform-model-inventory.md`;
that file is not the delivery SSOT. When you need a focused before/after
signal without rerunning the full refresh:

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/pricing/probe-antigravity-gemini25pro-literal.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --timeout-seconds 180
```

Optional: `PROBE_MODELS='gemini-2.5-pro'` or
`PROBE_ANTIGRAVITY_SOURCE_GROUP='Google-Gemini'`.

See also `.cursor/skills/tokenkey-online-log-troubleshooting` for the prod
read-only access posture and `ops/observability/run-probe.sh` for the SSM
transport. Probe-shape gotchas (claude-cli UA gate, `metadata.user_id` string,
codex `/v1/responses`) are documented in the probe script header.
