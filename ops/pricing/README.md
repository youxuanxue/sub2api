# TokenKey model-surface operations

This directory contains deterministic model-surface and pricing tools. Delivery
eligibility is owned only by
`docs/approved/pricing-serving-single-source-of-truth.md`; probes, traffic, discovery and
availability are evidence rather than request gates.

CLI syntax is owned by each parser. Run the tool with `--help`; the modelops and mapping
manager parsers are also generated into `docs/agent_integration.md` and checked by
`scripts/export_agent_contract.py --check`.

## Owner map

| Concern | Owner |
| --- | --- |
| Global prices and executable global pricing policy | `backend/internal/service/tk_pricing_overlay.json` |
| Scoped commercial override | `channel_model_pricing` |
| Curated newapi intent | `backend/internal/service/tk_served_models.json` |
| Release mapping floor | generated `model-surface-bundle.json` |
| Live account mapping | `accounts.credentials.model_mapping` plus optional reviewed runtime replacement |
| Native catalog/menu empirical projection | `pricing_catalog_supported_models_tk.go` |
| Generation route legality | `protocolrouter` and endpoint-capability owner |
| Current capacity and schedulability | scheduler/runtime owners |

No tool in this directory may collapse those owners into a persistent `deliverable` table.

## Tools

| Tool | Role |
| --- | --- |
| `modelops.py plan` | Read-only comparison of discovery, probe, price, manifest and live mapping evidence. |
| `modelops.py activate` | Prod-only, evidence-backed activation of a generated mapping floor. Defaults to validation and dry-run. |
| `refresh-servable-allowlist.py` | Probe and rewrite the shared native catalog/menu empirical sets. |
| `probe-servable-models.sh` | Minimal real requests with machine-readable verdicts. |
| `probe_reserved_resources.sh` | Guarded reusable probe groups/keys and cleanup. |
| `probe-traffic-proven-models.sh` | Positive-only recent traffic evidence. |
| `probe-antigravity-gemini25pro-literal.sh` | Focused Antigravity chat-vs-v1beta diagnostic; review evidence only. |
| `manage-account-model-mapping-runtime.py` | Validate, inspect and explicitly converge reviewed runtime/account mapping state. |
| `../newapi/apply-model-mapping-live.py remove-live` | Guarded emergency removal only; cannot add or rewrite mapping keys. |
| `model_surface_bundle.py` | Bundle schema and digest validation; owns no model list. |
| `pricing-registry-sensor.py` | Provider comparison evidence and registry-only PR candidate generation. |
| `manage-overlay-runtime.py` | Protected-main registry publication and read-only runtime audit. |
| `apply-pricing-hotfix.py` | Provider lookup evidence and explicitly scoped channel-price remediation. |
| `audit-display-coverage.py` | Live display-closeout audit paired with the static gate. |

Provider-specific OpenRouter helpers are documented in
`ops/pricing/openrouter-provider-onboarding.md` and are not modelops entrypoints.

## Read-only planning

Start model-surface work with the `tokenkey-modelops-planner` skill, then use
`modelops.py plan`. For volatile pools, capture a fresh live snapshot by account property
such as `channel_type`; do not copy account IDs from docs or skills.

Planner classifications are directions, not writes:

- `probe_needed`: run the emitted probe family.
- `price_missing`: collect official evidence and change the correct price owner.
- `ready_for_onboard`: update curated intent and build a target bundle.
- `mapping_missing` or mirror drift: review the bundle/live diff; do not hot-merge an
  individual key as a replacement for activation.
- `surfaces.catalog_menu`: use the catalog refresh branch separately.

## New-model activation

New or retargeted required mapping keys use `modelops.py activate` with:

- current and target checksummed bundles;
- fresh independent account-path probe evidence;
- fresh independent pricing evidence;
- a reviewed prod mapping plan;
- the fixed CLI confirmation phrase for the write.

The evidence envelope and 24-hour freshness contract live in
`docs/approved/model-surface-activation-contract.md`. Generic deploy and rollback never
write live mappings and do not depend on mapping convergence.

## Catalog/menu refresh

The public pricing catalog and per-user menu consume one empirical projection. The refresh
script owns candidate derivation, probe dispatch, result parsing and marker-delimited Go
splicing.

Safety invariants:

- successful traffic can skip a duplicate probe; absent traffic cannot mark a model
  unsupported;
- `--skip-video` carries existing video entries forward because video probing may create
  paid tasks;
- 401/403 is setup/auth failure, and 429/5xx/timeout is inconclusive;
- local `Unsupported model` or empty-pool results do not prove upstream inability;
- automatic splice coverage is defined by the script. Other platform results stay review
  evidence until the script explicitly supports them;
- source groups, accounts and deployable edges are resolved from live/machine owners, not
  copied into runbooks.

Validate with the refresh selftest, inspect the Go diff, then run the focused catalog
service tests before opening a PR.

## Runtime mapping

`manage-account-model-mapping-runtime.py` consumes a generated bundle. Its runtime JSON is
a scope replacement, not an incremental patch.

- Default post-release account checks are prod-only and read-only.
- Edge empty mappings are expected in routine checks.
- `sync-runtime` changes only the runtime replacement setting.
- Persistent account changes require an explicit reviewed apply.
- `release-gate` belongs only to deliberate model activation.
- A live runtime replacement that shadows the target bundle blocks activation.

For emergency removal of a bad live newapi mapping key, use the guarded removal-only tool
under `ops/newapi/`; correct the repository owner in the same change so a later release
cannot restore the key.

## Pricing remediation

Interpret `PricingMissingNotifier` by reason:

- `gate_rejected_unpriced`: request was rejected and not served;
- `served_at_fallback`: request was billed at a family floor, not at `$0`;
- `unpriced` or `negative_multiplier`: a true zero-cost leak path needs urgent correction.

Global price changes modify only the complete registry and publish through protected
`main`. Provider/LiteLLM data is evidence, never effective billing input. Use
`channel_model_pricing` only for a deliberately scoped commercial override; it must not be
used as a second global registry.

`manage-overlay-runtime.py sync-runtime` is reserved for the protected publisher workflow.
Operators use its read-only check mode. Registry rollback is a Git revert followed by the
same protected publisher.

## Mechanical gates

Relevant preflight checks include:

- complete pricing-registry validation and publication ownership;
- catalog/manifest/price/mapping-path drift;
- generated model-surface bundle drift;
- refresh and modelops selftests;
- pricing/serving document ownership;
- generated agent CLI contract drift.

Run `./scripts/preflight.sh` at the repository exit gates defined by project rules.
