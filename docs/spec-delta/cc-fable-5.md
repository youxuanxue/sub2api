# spec-delta: Fable 5 (`claude-fable-5`) support

TokenKey support for Anthropic's new top-tier model **Fable 5** (`claude-fable-5`,
the tier above Opus; $10/$50 per MTok, 1M context, 128K output). Grounded in real
Claude Code 2.1.170 traffic captured via the cc-fingerprint pipeline.

## Capture (ground truth)

- Tooling: `/tokenkey-cc-fingerprint-alignment` + cc0-here (cc0 → gost → SOCKS).
- Client: cc 2.1.170; capture egress **3.148.79.145**; 2026-06-09 (captured + re-verified).
- Driven by overriding `TOKENKEY_CC_CAPTURE_MODEL` through the mitm chain across
  haiku-4-5 / sonnet-4-6 / opus-4-8 / fable-5; a direct
  `cc0-here --model claude-fable-5` returned a real `PONG` (200), confirming the
  OAuth account can serve Fable today (free for Pro/Max/Team until 2026-06-22,
  then credits).

### Per-model `anthropic-beta` (real cc 2.1.170 `/v1/messages`)

| Model | beta set (relative to committed `sonnet_opus` baseline) |
|---|---|
| haiku-4-5 | distinct (`structured-outputs-2025-12-15`, no `claude-code`/`effort`); A/B bimodal per #429 |
| sonnet-4-6 | baseline **+ `effort-2025-11-24`** |
| opus-4-8 | baseline + `effort-2025-11-24` **+ `mid-conversation-system-2026-04-07`** |
| **fable-5** | **opus-4-8 + `server-side-fallback-2026-06-01` + `fallback-credit-2026-06-01`** (`opus-4-8 − fable-5 = ∅`) |

Fable is a **strict superset of Opus 4.8** — exactly the two fallback/credit betas
extra. TLS ja3 / UA (`claude-cli/2.1.170 (external, sdk-cli)`) /
X-Stainless-Package-Version (`0.94.0`) are identical across all four models.

### Findings

| Dimension | Fable 5 | vs Opus 4.8 | Action |
|---|---|---|---|
| TLS ja3 | matches baseline | ClientHello is model-independent | none |
| User-Agent | `claude-cli/2.1.170 (external, sdk-cli)` | identical | none |
| X-Stainless-Package-Version | `0.94.0` | identical | none |
| mimicry family | selector is `Contains(model,"haiku")` → else sonnet_opus; fable is non-haiku | auto-classified into sonnet_opus | none (already correct) |
| `anthropic-beta` (main `/v1/messages`) | Opus 4.8 set **+ `server-side-fallback-2026-06-01` + `fallback-credit-2026-06-01`** | only +2 fallback/credit betas | see Decision 3 |

Real captured Fable 5 beta set:

```
claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,
thinking-token-count-2026-05-13,context-management-2025-06-27,
prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,
advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24,
server-side-fallback-2026-06-01,fallback-credit-2026-06-01,
extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07
```

## Decisions

1. **Pricing (止血).** Fable was un-priced everywhere; `getFallbackPricing` matched
   the generic `claude→sonnet` catch-all → ~3.3x **underbill**. Added
   `claude-fable-5` = $10/$50 (cache write $12.50, cache read $1.00) to both
   `tk_pricing_overlay.json` (primary, litellm mirror lags) and
   `billing_service.go` `fallbackPrices` (safety net), plus an explicit `fable`
   branch in `getFallbackPricing` ahead of the claude catch-all.

2. **Thinking integrity.** Fable shares Opus 4.7+'s adaptive-only surface
   (`thinking:{type:"enabled",budget_tokens:N}` → 400, only `adaptive` accepted),
   but the reactive repair was hard-gated on `isOpus47OrNewer`, whose name/regex
   only match opus/sonnet/haiku — so Fable's adaptive-required 400 was gated OUT
   and surfaced raw. Added `isFableModel` + `requiresAdaptiveOnlyThinking`
   (`gateway_request_tk_fable.go`) and swapped the gate at the 3 call sites
   (`RectifyThinkingBudget` + the two reactive `RectifyThinkingTypeAdaptive`
   sites). Bedrock CC-compat path (`sanitizeBedrockThinking`, proactive) extended
   to treat Fable as adaptive-only **and** strip an explicit
   `thinking.type:"disabled"` — Fable's one breaking change beyond Opus 4.7+ (it
   400s on `disabled`; omitting the field is the documented equivalent).

   **Known gap (deferred):** the *direct* Anthropic path has no proactive thinking
   sanitizer (it is reactive-on-400 only), and the exact upstream 400 message for
   Fable `disabled` was not captured (cc never emits `disabled` for Fable). So a
   third-party client sending `thinking:{type:"disabled"}` for Fable on the direct
   path gets an honest passthrough 400 rather than an auto-repair. Add a reactive
   matcher once the real message is captured.

3. **Mimicry betas — intentionally NOT changed.** Fable already auto-classifies
   into the sonnet_opus mimicry family, so it inherits that beta set. The two
   Fable-only betas (`server-side-fallback-2026-06-01`, `fallback-credit-2026-06-01`)
   are first-party credit/fallback-billing signals; injecting them on relayed
   accounts is meaningless-to-harmful, so they are recorded here but not added to
   the mimicry constants.

   Separately, re-verification shows the committed `sonnet_opus` baseline lacks
   `effort-2025-11-24` (sonnet-4-6 **and** opus-4-8) and
   `mid-conversation-system-2026-04-07` (opus-4-8) that live cc 2.1.170 sends — a
   **general 2.1.170 beta drift**, independent of Fable, and likely
   request-conditional (those betas only appear when effort / mid-conversation
   system features are exercised, so a flat hard-align risks over-fitting one
   sample — cf. #429). To be handled by the normal cc-fingerprint-alignment flow
   (§4.2) with multi-request characterization, not this PR.

4. **Visibility (all three).** Added `claude-fable-5` to
   `claude.DefaultModels` (advertised `/v1/models` + admin pickers),
   `supportedAnthropicCatalogModels` (public `/pricing` + Your-Menu; justified by
   the live 200 probe — the next `refresh-servable-allowlist.py` run reconciles
   it), and the frontend `claudeModels` + label/color map (`useModelWhitelist.ts`).

## Out of scope

General cc 2.1.170 `sonnet_opus` beta drift (Decision 3) and any direct-path
`disabled` reactive repair (Decision 2).
