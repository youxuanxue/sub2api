---
title: Zero-usage estimate fallback SSOT（usage 全 0 估算兜底单一事实源）
status: approved
approved_by: "product owner (2026-11-04 conversation: 如果上游就返回 usage 为 0，我们网关可以通过估算兜底吧。不然就亏了。); product owner (2026-11-05 conversation: 【usage 全 0 估算兜底】，应该全平台全模型统一吧？遵循 ssot 原则。); feng (2026-11-12 conversation: 对着 origin/main 检查 2 的用量问题，尽可能修复。)"
approved_at: 2026-11-05
created: 2026-11-05
authors: [claude]
risk: high
---

# Zero-usage estimate fallback SSOT

Scope: every platform and every text model the gateway bills by tokens. When a
successful response delivered output but reported no billable usage, the
gateway bills a local estimate instead of $0. One owner decides, counts,
records evidence and fills client usage. Protocol code only extracts text.

## Rule

1. Real upstream usage always wins. Any non-zero billable bucket disables the
   estimate: input, output, cache read, cache creation (incl. 5m/1h), image
   input, image cache read, image output.
2. The response must be a delivered success: HTTP status below 400, no in-band
   stream error (`error` / `response.failed` frame or a recorded ops stream
   error), forward returned no error, client did not disconnect.
3. Delivered output must be non-empty. An empty answer (silent refusal class)
   is not estimated and keeps the existing refusal handling.
4. Media results are never estimated (see per-modality table).
5. Native estimators are skipped, so nothing is estimated twice.
6. Grok non-stream missing usage still fails over before any write
   (`grok_missing_usage`). The owner only sees Grok results that reach a
   billing choke point, which is the stream case where failover is no longer
   possible.
7. The Grok failover guard keeps its own, stricter predicate
   (`hasBillableGrokChatUsage`: aggregate input/output/cache buckets only).
   Detail-only image tokens do not prove a Grok response can be settled, so it
   still fails over; the estimate owner treats any non-zero bucket, image
   included, as real usage and never overrides it.
8. Held client bytes leave at the billing choke point, whether or not an
   estimate applies: both hooks release the capture writer before returning.
   Work after the choke point (usage submission runs inline when the record
   pool overflows under the default `sync` policy) never delays delivery.
   Paths that never reach a choke point are released when the handler returns.

Estimate: input = cl100k count of the prompt text the upstream received (final
outbound body when a path stashed it, else the ops upstream wire body, else the
inbound body; Gemini cloudcode `{"request":…}` envelopes are unwrapped). Output
= cl100k count of the delivered output text. No multiplier, no cache split.

## Owners

| Role | File |
| --- | --- |
| Decision, counting, ops event, log, OpenAI-path tier persistence | `backend/internal/service/usage_estimate_fallback_tk.go` |
| Delivered-output capture and client usage writer | `backend/internal/service/usage_estimate_capture_tk.go` |
| Protocol text adapters (OpenAI chat, OpenAI responses, Anthropic messages, Gemini) | `backend/internal/service/usage_estimate_text_tk.go` |
| Route wiring (every gateway chain) | `backend/internal/server/routes/gateway.go` (`endpointNorm := service.WithUsageEstimateCapture(...)`) |
| Contract test | `backend/internal/service/usage_estimate_fallback_contract_tk_test.go` |
| Sentinels | `scripts/sentinels/gateway-tk.json` |
| Shared counter | `backend/internal/pkg/tokenestimate/count.go` |

Native estimators registered in the owner (`usageEstimateNativeEstimator`):

| Platform | Owner | billing_tier |
| --- | --- | --- |
| Kiro | `integration/kiro/estimate.go`, `service/kiro_gateway_service_tk_cache_billing.go` | `kiro-estimated` / `kiro-cache-estimated` (dashboards filter on `kiro-estimated`) |
| Gemini Web | `integration/geminiweb/estimate.go`, `service/gemini_web_usage_estimate_tk.go` | `gemini-web-estimated` |

They were not folded in. Kiro has no upstream usage at all and splits a
prompt-cache estimate from its own request model; Gemini Web estimates inside
the service before a ForwardResult exists. Moving either would change numbers
or tags, so both stay native and the generic owner skips their accounts.

### Billing choke points

Every production `RecordUsage` / `RecordUsageWithLongContext` submit site is
either one of these (calls the owner before submitting) or a declared non-text
site. The contract test fails on any unclassified site.

| Choke point | Hook | Routes / platforms |
| --- | --- | --- |
| `handler/gateway_handler.go` `submitForwardUsage` | `ApplyUsageEstimateFallback` | `/v1/messages`, `/antigravity/v1/messages`: Anthropic OAuth/setup/API key, Bedrock, Vertex, Antigravity, Kiro (native skip), Claude-compat converters |
| `handler/gateway_handler_tk_usage.go` `tkSubmitClaudeGatewayForwardUsage` | `ApplyUsageEstimateFallback` | Claude-gateway `/v1/chat/completions` and `/v1/responses` (Anthropic, Kiro, Bedrock, Antigravity, Gemini compat) |
| `handler/gateway_handler_tk_messages_gemini.go` | `ApplyUsageEstimateFallback` | Gemini-platform `/v1/messages` |
| `handler/gemini_v1beta_handler.go` | `ApplyUsageEstimateFallback` | `/v1beta/models/*:generateContent` / `:streamGenerateContent` (Gemini, Antigravity, Vertex; Gemini Web native skip) |
| `handler/openai_gateway_handler_tk_forward_usage.go` `tkSubmitHTTPForwardUsage` | `ApplyOpenAIUsageEstimateFallback` | OpenAI-compatible `/v1/chat/completions`, `/v1/responses`, `/backend-api/codex/responses`, `/v1/messages`: OpenAI/Codex, newapi bridge and raw, Volc Agent Plan, Grok (stream), OpenRouter |

Declared non-text sites (not estimated): Responses WebSocket turns
(`openai_gateway_handler_tk_responses_ws.go`, no HTTP writer to capture),
images, web search, alpha search, explicit context cache, Grok audio/media,
System One, embeddings, video, cyber-policy usage log, managed batch
settlement, and `tkSubmitOpenAISimpleForwardUsage` (embeddings, images, audio
speech).

## Classification

| Platform / path | Protocol | Stream | Behavior |
| --- | --- | --- | --- |
| Anthropic, Bedrock, Vertex, Antigravity (Claude) | Anthropic messages | no | estimate billed; `usage` replaced in body |
| same | Anthropic messages | yes | estimate billed; `message_delta.usage` patched |
| any via Claude-gateway or OpenAI-compatible converter | OpenAI chat | no | estimate billed; `usage` replaced |
| same | OpenAI chat | yes | estimate billed; zero usage-only chunk patched in place, else one usage-only chunk added before `[DONE]` (or at end) |
| any | OpenAI responses | no | estimate billed; `usage` replaced |
| any | OpenAI responses | yes | estimate billed; `response.completed.response.usage` patched |
| Gemini, Antigravity, Vertex | Gemini generateContent | no | estimate billed; `usageMetadata` set |
| same | Gemini streamGenerateContent | yes | estimate billed; client usage not echoed (usageMetadata rides on content chunks; holding them would delay output) |
| newapi raw / Volc Agent Plan kimi-k3 | OpenAI chat | both | as OpenAI chat (Volc private logic removed) |
| Grok | OpenAI chat | no | fails over before write (unchanged) |
| Grok | OpenAI chat / responses | yes | estimate billed (failover impossible after write) |
| Kiro | any | both | native estimator, owner skips |
| Gemini Web | any | both | native estimator, owner skips |
| OpenAI Responses WebSocket | responses WS | yes | not estimated (no HTTP capture); billed as reported |
| Body over 8 MiB, or JSON flushed as a stream without `text/event-stream` | any | any | oversize: not estimated; flushed JSON: billed, not echoed |

Per modality:

| Modality | Effect |
| --- | --- |
| Text / tool calls / reasoning | estimated when rule 1-3 hold |
| Image input parts in a text request | K3 family (`kimi-k3`, `kimi-k3-*`, `kimi-k3.*`) data-URL images are added to input tokens using the MoonViT media expansion. Other models, and K3 images whose size cannot be read locally (https URLs), are not counted |
| Image generation (count/size) | never estimated (`ImageCount > 0`, image routes not captured) |
| Image output tokens | any non-zero value is real usage, wins |
| Video | never estimated (`VideoCount > 0`, video routes not captured) |
| Audio (TTS/STT/realtime) | never estimated (`AudioUsage != nil`, audio routes not captured) |
| Embeddings | never estimated (route not captured) |
| count_tokens / input_tokens / compact | never estimated (route not captured) |

## Marking and observation

- `usage_logs.billing_tier = usage-estimated` on both RecordUsage paths. On the
  OpenAI RecordUsage only this owner's tier is persisted (other OpenAI-path
  tiers keep their historical unpersisted behavior). billing_tier is a label:
  price resolution does not read it (`TestUsageEstimateTierDoesNotChangePrice`).
- One ops upstream event per estimate: kind `usage_estimated`, with platform,
  account id/name, proxy, upstream request id, and `Detail` carrying model,
  protocol, stream and the estimated counts. It is kept on the recovered
  upstream row for successful requests.
- One structured warn log `gateway: usage estimated` with the same fields.
- The estimate is priced once at the choke point (`previewClaudeClientUsageCost`
  / `previewOpenAIClientUsageCost`). That CostBreakdown replaces any zero-usage
  preview as `PrecomputedCost`, and its TotalCost is written as `usage.cost`
  into the patched client usage (OpenAI chat, responses, Anthropic messages).
  Client cost, precomputed settlement and usage_logs cost are one computation,
  including the chat usage chunk the owner inserts
  (`TestUsageEstimateCostMatchesBilling`, carried from R-001).

billing_tier decision: one generic value, `usage-estimated`, for every
estimate this owner mints. The former `volc-kimi-estimated` value and the
`volc_kimi_usage_estimated` event were unreleased (never on `main`, never
written in production) and are retired. `kiro-estimated`,
`kiro-cache-estimated` and `gemini-web-estimated` stay readable and unchanged.

## Accuracy

cl100k_base is not the upstream tokenizer for most models. On ordinary text the
error is unmeasured: cl100k counts about 1.1 tokens per English word and about
1.2 per Chinese character, and Claude, Gemini, Kimi and other tokenizers are
not public or not compared here. On the cn-prod benchmark's random 3–9 letter
word text, cl100k counted 3.45 tokens per word against 3.42 from real Kimi
usage, within about 1%. The estimate leaves out chat template overhead, video
input and cache hits. Image input is counted only for the K3 family data URLs
described above; every other image still errs low. Cache hits are billed at
the uncached input rate. A non-zero upstream usage is never replaced, so a
supplier tokenizer that already differs from the K3 reference (for example a
stable tool-template delta) stays as reported.

## Rollout and observation

1. Ship behind normal release; no flag. Estimates only happen where the
   gateway billed $0 before, so the change cannot lower any bill.
2. After deploy, the coordinator runs read-only daily:
   `SELECT account_id, model, count(*), sum(input_tokens), sum(output_tokens)
   FROM usage_logs WHERE billing_tier = 'usage-estimated' AND created_at >
   now() - interval '1 day' GROUP BY 1,2 ORDER BY 3 DESC;`
   and the matching ops events (`upstream_errors` JSON kind `usage_estimated`).
3. Watch for: a platform that suddenly estimates on most requests (upstream
   usage parsing regression on our side, not a supplier issue); report
   persistent zero-usage accounts to the supplier.
4. Pre-deploy baseline (read-only, to size the effect):
   `SELECT a.platform, ul.model, count(*) FROM usage_logs ul JOIN accounts a ON
   a.id = ul.account_id WHERE ul.created_at > now() - interval '7 days' AND
   ul.input_tokens = 0 AND ul.output_tokens = 0 AND ul.cache_read_tokens = 0
   AND ul.cache_creation_tokens = 0 AND ul.image_count = 0 AND
   ul.video_count = 0 AND COALESCE(ul.billing_tier,'') NOT LIKE '%estimated'
   GROUP BY 1,2 ORDER BY 3 DESC;`

## Validation

`backend/internal/service/usage_estimate_fallback_tk_test.go`: zero usage per
protocol and stream mode on both result types (billed, tagged, client usage
equals billed, one event), chat stream ordering and in-place patch, real usage
wins per bucket, empty output not estimated, errored/in-band-error/disconnect/
media/native-estimator skips, non-text routes not captured, held bytes count as
written, content streams before usage, flushed JSON released, held body
released at the choke point (estimated and real-usage cases), persistence on
both RecordUsage paths, tier does not change price, client usage.cost equals
billed cost, and the Volc raw Chat path end to end. Existing Kiro, Gemini Web
and Grok missing-usage tests are unchanged.
