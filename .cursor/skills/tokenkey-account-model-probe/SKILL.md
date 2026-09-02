---
name: tokenkey-account-model-probe
description: Probe one TokenKey prod or edge account against one model through the gateway or direct upstream path using reserved debug resources. Use to separate account capability, TokenKey routing, and usage attribution before modelops or scheduling changes.
---

# TokenKey account/model probe

Run probes through `ops/observability/run-probe.sh`; do not SSH manually. Account IDs,
names, groups and edge placement come from the current task or live snapshot and are
identifiers only, never capability metadata.

## Choose the evidence path

| Question | Script |
| --- | --- |
| Can TokenKey route this request to the intended account? | `ops/stage0/probe_account_model.sh` with `ops/pricing/probe_reserved_resources.sh` |
| Can the raw OpenAI/Grok credential serve it? | `ops/stage0/probe_direct_upstream_model.sh` plus the platform companion |
| What does an Antigravity or Anthropic account advertise/test upstream? | `ops/stage0/probe_account_upstream_models.sh` |
| Can a Kiro OAuth account serve a model through its runtime? | `ops/stage0/probe_kiro_upstream_models.sh` |
| Are Kiro thinking fields present? | `ops/stage0/probe_kiro_upstream_thinking_fields.sh` |
| What protocols does a CloudWise account expose? | `ops/stage0/probe_cloudwise_upstream_matrix.sh` |
| Does one CloudWise spelling work? | `ops/stage0/probe_cloudwise_model_case.sh` |
| Which Claude IDs does one Kiro account serve? | `ops/stage0/probe_kiro_claude_models.sh` |

For the canonical gateway path, pass the target, `ACCOUNT_ID`, `MODEL`, and the
operation-appropriate `ENDPOINT` to `probe_account_model.sh`. `run-probe.sh`
automatically ships the registered companions for every script in the table;
inspect its machine-owned contract before a less familiar path with
`--script <path> --describe-script`. Use `messages` for
Anthropic/Kiro, `chat` for OpenAI-compatible chat, `responses` for Codex/OpenAI
Responses, and `embeddings` for embedding SKUs. Read each script's header for optional
request-shape parameters; do not copy account-specific commands into this skill.

## Interpret the result

The contract printed by `--describe-script` is authoritative because direct, list,
matrix and batch probes intentionally have different verdict vocabularies. For the
canonical gateway path:

- `servable`: the request succeeded and, where usage attribution applies,
  `usage_match.account_id` is the requested account.
- `wrong_account`: the request succeeded through a different account.
- `gateway_rejected`: TokenKey rejected before usable upstream evidence, often because
  of the current mapping/floor, capacity, billing or request shape.
- `upstream_rejected`: the intended upstream account path was reached and rejected the
  credential/model/request combination.
- `uncorrelated_success`: HTTP succeeded but the usage row did not arrive in the bounded
  correlation window; inspect logs before trusting it.
- `setup_error`: probe resource or account setup failed; it is not a model verdict.

A local `Unsupported model`, empty pool or `account_id=null` is not proof that the
provider account lacks the model. A direct upstream probe proves raw account capability;
the gateway probe proves TokenKey-path serving and attribution. Model activation needs
both the appropriate upstream evidence and a gateway result attributed to the intended
account.

## Safety

- This is a live, low-cost request: it may consume quota, append usage rows, and update
  normal account recovery state.
- Reserved `__tk_probe_*` groups/keys stay exclusive and direct-probe-only. Default
  cleanup removes the temporary binding and disables the reusable resources.
- Do not print or paste credentials. Probe outputs must remain bounded and sanitized.
- Use the direct upstream path only for capability isolation; it does not authorize
  catalog, pricing, mapping or scheduling writes.
- Enter model-surface work through `tokenkey-modelops-planner`; use
  `tokenkey-kiro-reauth` when the evidence shows Kiro OAuth auth drift.
