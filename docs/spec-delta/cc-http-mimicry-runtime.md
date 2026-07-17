# spec-delta: runtime HTTP mimicry manifest (cc OAuth betas)

## Background

cc patch releases drift `anthropic-beta` every few days. Requiring a full image release for each capture alignment was too heavy (#438). UA already has a runtime path (`claude_code_user_agent_version`); betas did not.

## Delta

### ADDED

- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` — canonical sonnet_opus + haiku beta lists.
- `settings.claude_code_http_mimicry_manifest` — JSON upserted by `sync-runtime` / `cc_fingerprint_apply_http_runtime.sh`.
- `GetClaudeCodeMimicryBetas` + `GetFullClaudeCode*MimicryBetasForContext` — gateway reads runtime manifest, falls back to compile-time.
- `manage-anthropic-config.py plan-http-mimicry-sync` — audit plan only; apply via `sync-runtime`.

### MODIFIED

- `sync-runtime` / `apply --sync-runtime` — also upsert HTTP mimicry manifest (with UA + Redis fingerprint DEL).

## Scenarios

### Core positive

1. Given merged PR updates `anthropic-http-mimicry-baselines.json`, when operator runs `cc_fingerprint_apply_http_runtime.sh`, then deployable edges + prod have matching settings and next OAuth forward uses new betas without redeploy.

2. Given empty manifest setting, when gateway merges OAuth mimicry betas, then compile-time `FullClaudeCode*MimicryBetas()` is used.

### Core negative

1. Given invalid manifest JSON in settings, when getter runs, then ok=false and compile-time fallback applies.

## Validation

```bash
go test -tags=unit ./internal/pkg/claude/... -run 'Mimicry|FullClaudeCode'
go test -tags=unit ./internal/service/... -run 'ClaudeCodeHTTPMimicry'
python3 -m unittest ops.anthropic.test_manage_anthropic_config_runtime_sync -v
```
