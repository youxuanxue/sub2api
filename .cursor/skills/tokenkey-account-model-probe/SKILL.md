---
name: tokenkey-account-model-probe
description: Probe a specific TokenKey prod or edge account against one model by reusing reserved __tk_probe_* debug resources. Use when debugging whether account_id N on prod/edge can actually serve a model, when isolating routing from account capability, or when checking a suspect prod/edge account before changing scheduling.
---

# TokenKey Account Model Probe

Run a live, low-cost probe for one account and one model. Default path reuses one reserved exclusive `__tk_probe_<platform>_group` and `__tk_probe_<platform>_key` per target host/platform, temporarily binds only the target account, sends one real gateway request, then removes the account binding and disables the probe resources. This avoids accumulating or leaving active one-off debug groups and keys.

Use `ops/observability/run-probe.sh`; do not SSH manually.

## Quick Run

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/stage0/probe_account_model.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env ACCOUNT_ID=<account_id> \
  --env MODEL=<model> \
  --env ENDPOINT=messages
```

For an edge account:

```bash
bash ops/observability/run-probe.sh \
  --target edge:<edge_id> \
  --script ops/stage0/probe_account_model.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env ACCOUNT_ID=<account_id> \
  --env MODEL=<model> \
  --env ENDPOINT=messages
```

Useful options:

- `ENDPOINT=messages|chat|responses` chooses `/v1/messages`, `/v1/chat/completions`, or `/v1/responses`.
- `MAX_TOKENS=16` keeps cost low.
- `PROMPT_TEXT='Reply OK only.'` changes the prompt.
- `REQUEST_EXTRA_JSON='{"temperature":0.7,"top_p":0.9}'` merges extra top-level JSON fields into the generated payload for request-shape compatibility probes.
- `REQUEST_TIMEOUT_SECONDS=90` caps the in-container gateway request.
- `PROBE_REUSE_MODE=1` is the default: reuse `__tk_probe_<platform>_group` / `__tk_probe_<platform>_key`.
- `PROBE_REUSE_MODE=0` creates a one-off `__tk_probe_tkprobe-*` group/key and soft-deletes it on cleanup.
- `KEEP_PROBE_ARTIFACTS=1` keeps the current account binding for manual follow-up. In one-off mode it also keeps that temporary group/key; in reuse mode it leaves the reserved group/key active until manual cleanup. Default is cleanup.
- `PROBE_LOCK_TIMEOUT_SECONDS=120` caps waiting for another same-platform reuse probe to finish.
- `APP_CONTAINER=tokenkey APP_URL=http://localhost:8080` are the default in-container request target.

## How To Read It

The script emits one JSON object. Treat these fields as the decision surface:

- `verdict=servable`: HTTP 2xx and `usage_logs` confirms `account_id == ACCOUNT_ID`.
- `verdict=wrong_account`: request succeeded but usage correlation shows a different account. This means the probe pool was not isolated or sticky/routing interfered.
- `verdict=gateway_rejected`: TokenKey rejected before upstream, usually model unsupported, no available accounts, billing/RPM, or request shape.
- `verdict=upstream_rejected`: upstream reached but rejected auth/model/request.
- `verdict=uncorrelated_success`: HTTP 2xx but no usage row was found in the poll window; inspect recent logs before trusting it.
- `verdict=setup_error`: target account/group/key setup failed; not a model signal.

Never paste returned API keys or credentials. The script intentionally prints IDs, names, status, short body excerpts, and log excerpts only.

### OpenAI OAuth model-gate interpretation

For OpenAI OAuth/Codex-compatible accounts, distinguish local TokenKey gates from upstream account capability:

- `gateway_rejected` with body like `Unsupported model: <id>` usually means TokenKey's current
  account `model_mapping` / compiled floor rejected the model before a usable upstream call. This is
  not proof that an edge OAuth account can or cannot serve the model.
- `upstream_rejected` with body like `not supported when using Codex with a ChatGPT account`
  means the OpenAI OAuth account path reached upstream and the upstream account/model combination
  rejected it.
- A model is promotable to catalog/Menu or runtime `model_mapping` only after a single-account
  probe returns `verdict=servable` and `usage_match.account_id == ACCOUNT_ID`.

Example from 2026-07-08: prod normal probes for `gpt-5.6*` returned local
`Unsupported model` with `account_id=null`; edge OpenAI OAuth accounts on `edge:us4` and `edge:us3`
then reached upstream but returned `The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account.`
So clearing prod model_mapping/floor alone would not make `gpt-5.6` servable.

## Rules

- Prefer this skill over creating permanent admin groups for single-account debugging.
- This is a live probe. It may consume a tiny amount of quota and can update normal usage tables.
- Default reuse mode intentionally leaves at most one reserved probe group/key per platform on each target host, disables it after cleanup, and removes account binding after the request.
- Probe groups must stay exclusive, grant `user_allowed_groups` only to the probe key owner, and remain direct probe-key only; they must not enter universal-key routing candidates.
- Use `ENDPOINT=messages` for Anthropic/Kiro style accounts, `chat` for OpenAI-compatible chat, and `responses` for Codex/OpenAI responses.
- If the goal is "can the raw upstream credential serve this model" for an API-key-compatible account, direct upstream curl can be a follow-up, but the default gateway probe is the authoritative TokenKey-path proof.

## Follow-Up Checks

If the verdict is not `servable`, inspect:

```bash
# Recent account-specific ops errors/logs on the same target
bash ops/observability/run-probe.sh \
  --target <prod|edge:id> \
  --script ops/observability/probe-caps.sh \
  --env PLATFORM=<platform> \
  --env ERR_HOURS=1
```

For Kiro OAuth auth drift, switch to `tokenkey-kiro-reauth`. For any modelops work (catalog refresh, mapping drift, onboard), enter via `tokenkey-modelops-planner` first.

## Batch Kiro Claude model matrix

When validating which Claude ids a Kiro account (native edge OAuth or prod mirror stub) actually serves, run the batch wrapper (loops `probe_account_model.sh`):

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/stage0/probe_kiro_claude_models.sh \
  --with ops/stage0/probe_account_model.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env ACCOUNT_ID=66
```

Override the default model list with `MODELS="claude-haiku-4-5 claude-opus-4-8 ..."` if needed.
