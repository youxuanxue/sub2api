# Model Release Watch

## Background

`client-release-watch` discovers CLI pin drift. TokenKey also needs timely discovery of
vendor model launches (OpenAI / Anthropic / Google / GLM / Kimi / DeepSeek / Doubao) so
operators can triage onboarding. Anthropic supply is multi-channel (OAuth, Kiro, Cursor
ct14, Tokensea, CloudWise, Bedrock), so absence from the native Anthropic OAuth allowlist
alone is not “not served”.

## Delta

- ADDED: `ops/pricing/model-release-public-catalog.json` — committed public-channel
  model ID seeds (no API keys; works when vendor docs are geo-blocked locally).
- ADDED: `ops/pricing/model_release_watch.py` — repo SSOT baseline + public catalog
  seeds + live docs/changelog/blog fetch when reachable + optional API enrichment +
  classification (`served` / `unpriced` / `missing` / `narrow` / `watch_only` /
  `out_of_scope`).
- ADDED: current-generation scope filter:
  - Anthropic Claude 5.x / OpenAI GPT-6+ & gpt-image-2 / GLM-5.x / Kimi K3+ /
    Doubao Seed 2.1+
  - **Google (locked, 2026-10-03 converge):**
    1. Text Flash: `gemini-3.8-flash` and newer Flash SKUs
    2. Pro: newer than frozen `gemini-3.1-pro*` (`>= 3.2` Pro). `3.1-pro` /
       `-preview` / `-high` / `-low` are intentional non-public / structural-deadlist
    3. Image: `gemini-3.1-flash-image` and newer **non-lite** image SKUs, plus
       Nano Banana aliases (`nano-banana-2` / `nano-2` / `nano-banana-pro*`) and
       Nano Banana Pro (`gemini-3-pro-image`). Lite image not expanded.
  - Out of scope examples: Gemini ≤3.7 Flash text, `3.1-flash-lite`, lite-image,
    3.1 Pro line, embedding, Veo, Live/TTS, bare `gemini-3` prose fragments.
- ADDED: seen-state novelty filter (`.cache/model-release-watch/state.json`). Empty state
  auto-bootstraps without opening issues; later runs alert only on newly seen in-scope gaps.
- ADDED: `ops/pricing/open_model_release_watch_issues.py` — open/update/close GitHub issues
  for `actionable=true` findings only.
- ADDED: `.github/workflows/model-release-watch.yml` — daily scan; **issue sync opt-in** via
  `workflow_dispatch.open_issues=true` (schedule never opens issues by default).
- MODIFIED: “served” means an **explicit** requestable key on any supply surface **and** a
  price owner. Wildcard mappings (e.g. CloudWise `claude-*`) do not count.
- MODIFIED: Primary surfaces for full coverage include anthropic / openai / gemini /
  antigravity / kiro / tokensea / cloudwise (explicit) / cursor ct14 / curated manifest /
  native allowlists. Bedrock-only priced mappings classify as `narrow`.

## Scenarios

- Positive: upstream `claude-opus-5-5` with Kiro mapping + overlay price → `served`, no issue.
- Negative: new upstream `claude-sonnet-5-5` with Bedrock mapping but no overlay price →
  `unpriced` actionable (after state is bootstrapped).
- Negative: `gemini-3.5-flash` / `gemini-3.1-flash-lite` / `gemini-3.1-pro*` /
  `gemini-3.1-flash-lite-image` / embedding / Veo → `out_of_scope`.
- Positive scope: `gemini-3.8-flash` / `gemini-3.1-flash-image` / `nano-banana-2` /
  future `gemini-3.2-pro+` stay in-scope.
- Regression: CloudWise `claude-*` must not mark a brand-new Claude id as served.
- Bootstrap: first scan with empty state seeds seen ids and suppresses issues.
- Watch-only: Gemini 4 Argon without public API id → report only, no issue.

## Validation

- `python3 ops/pricing/model_release_watch.py --selftest`
- `python3 -m unittest ops.pricing.test_model_release_watch ops.pricing.test_open_model_release_watch_issues -v`
- Manual: `bash ops/pricing/model-release-watch.sh scan` then inspect
  `.cache/model-release-watch/report.md`
