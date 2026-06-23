---
name: tokenkey-account-model-probe
description: Probe a specific TokenKey prod or edge account against one model without creating permanent debug groups. Use when debugging whether account_id N on prod/edge can actually serve a model, when isolating routing from account capability, or when checking a suspect prod/edge account before changing scheduling.
---

# TokenKey Account Model Probe

Run a live, low-cost probe for one account and one model. Default path creates a temporary exclusive `__tk_probe_*` group and API key on the target host, routes one real gateway request through a pool containing only the target account, then cleans the artifacts.

Use `ops/observability/run-probe.sh`; do not SSH manually.

## Quick Run

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script .cursor/skills/tokenkey-account-model-probe/scripts/probe_account_model.sh \
  --env ACCOUNT_ID=<account_id> \
  --env MODEL=<model> \
  --env ENDPOINT=messages
```

For an edge account:

```bash
bash ops/observability/run-probe.sh \
  --target edge:<edge_id> \
  --script .cursor/skills/tokenkey-account-model-probe/scripts/probe_account_model.sh \
  --env ACCOUNT_ID=<account_id> \
  --env MODEL=<model> \
  --env ENDPOINT=messages
```

Useful options:

- `ENDPOINT=messages|chat|responses` chooses `/v1/messages`, `/v1/chat/completions`, or `/v1/responses`.
- `MAX_TOKENS=16` keeps cost low.
- `PROMPT_TEXT='Reply OK only.'` changes the prompt.
- `REQUEST_TIMEOUT_SECONDS=90` caps the in-container gateway request.
- `KEEP_PROBE_ARTIFACTS=1` preserves the temporary group/key for manual follow-up. Default is cleanup.
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

## Rules

- Prefer this skill over creating permanent admin groups for single-account debugging.
- This is a live probe. It may consume a tiny amount of quota and can update normal usage tables.
- Default cleanup must leave no persistent debug group/key. If `KEEP_PROBE_ARTIFACTS=1`, delete them manually after debugging.
- Probe groups must stay exclusive and must not be added to `user_allowed_groups`; they are direct probe-key only and must not enter universal-key routing candidates.
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

For Kiro OAuth auth drift, switch to `tokenkey-kiro-reauth`. For catalog-wide model serving refresh, use `tokenkey-servable-model-refresh`.
