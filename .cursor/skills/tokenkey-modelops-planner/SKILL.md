---
name: tokenkey-modelops-planner
description: >-
  Route TokenKey model-surface operations across read-only planning, catalog refresh,
  evidence-backed onboarding, and reviewed runtime mapping changes. Use for model/menu
  drift, empty pools, mapping drift, or new-model activation; not for protocol routing.
---

# TokenKey model-surface operations hub

Use this skill to choose the owner and write path. It does not own delivery eligibility;
that contract remains in `docs/approved/pricing-serving-single-source-of-truth.md`.

CLI syntax is owned by the argparse parsers and generated into
`docs/agent_integration.md` by `scripts/export_agent_contract.py`. Read that generated
CLI section or run the relevant script with `--help`; do not copy parameter tables here.

## Route the request

| Intent or symptom | Owner |
| --- | --- |
| Compare discovery, probes, prices, manifest and live mapping | `modelops.py plan` — read-only |
| Refresh public `/pricing` and user-menu empirical sets | `tokenkey-servable-model-refresh` |
| Add a curated newapi model | `tokenkey-onboard-model` |
| Validate or converge runtime/account mapping | `manage-account-model-mapping-runtime.py` |
| Activate a new bundle floor | `modelops.py activate` |
| Probe one account and one model | `tokenkey-account-model-probe` |

Protocol capability and route legality belong to `protocolrouter` and
`docs/approved/protocol-routing-ssot.md`, not this workflow.

## Decision boundaries

- `plan` is read-only. Discovery, probe and traffic are evidence; none may write catalog,
  price or mapping owners automatically.
- A gateway `Unsupported model`, empty pool or `account_id=null` may be a local floor
  rejection. It is not upstream capability evidence.
- Before promotion, prove the upstream/account capability and then prove the TokenKey
  account path (`usage_match.account_id` must match the intended account).
- New or retargeted mapping floors use a generated bundle plus fresh independent probe
  and pricing evidence through `modelops.py activate`. Do not hot-merge a single mapping
  key as a substitute for activation.
- `sync-runtime` and `clear-runtime` accept one explicit `prod` or `edge:<id>` target.
  They default to dry-run and require the fixed CLI confirmation phrase to write.
  Persistent account changes require a separate reviewed `apply-accounts` diff.
- Routine post-release checks target prod. Edge empty mappings are expected unless an
  explicit troubleshooting task includes edges.
- Catalog/menu/pricing/mapping tests derive samples from their owners; do not maintain a
  second model list in tests or skills.

## Human judgment

Pause for human approval before any repository merge or prod/edge write. Treat 401/403,
429 and 5xx probe results as inconclusive until account, request shape and capacity have
been separated from model capability.

## Owners

- Delivery composition: `docs/approved/pricing-serving-single-source-of-truth.md`
- Activation evidence: `docs/approved/model-surface-activation-contract.md`
- Tool roles and invariants: `ops/pricing/README.md`
- Generated CLI contract: `docs/agent_integration.md` § CLI
