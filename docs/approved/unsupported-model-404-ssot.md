---
title: Unsupported Model 404 SSOT（不支持/未知模型统一 404）
status: approved
approved_by: "feng (2026-11-04 conversation: 不支持的模型，返回 404 也需要全局改一下吧，保持 ssot 原则。)"
approved_at: 2026-11-04
created: 2026-11-04
authors: [claude]
risk: medium
---

# Unsupported Model 404 SSOT

## Decision

When the gateway itself decides that a requested model is unknown or not served here, it returns **HTTP 404**
in the official shape of the inbound protocol. This replaces the old local `400 invalid_request_error "Unsupported model: X"`.
One owner defines the status and every wire shape. Emit sites never hardcode 400 or 404 for this case.

## Owner

`backend/internal/service/gateway_service_tk_unsupported_model.go`

| Symbol | Role |
| --- | --- |
| `TkUnsupportedModelStatus` (`404`), `TkUnsupportedModelErrType`, `TkUnsupportedModelCode` (Anthropic/Gemini type is internal) | Status and per-wire type/code |
| `TkUnsupportedModelMessage(model)` | Client copy, unchanged: `Unsupported model: X` |
| `TkUnsupportedModelErrorWithMessage` → `.Body()` | Official envelope for `TkErrorWireOpenAI` / `Anthropic` / `Gemini` |
| `TkWriteUnsupportedModel(c, wire, model)` | Direct write (middleware, universal routing) |
| `TkUnsupportedModelSelectionResponse(c, model)` / `...WithMessage` | `(status, type, message)` tuple for handler writers; marks the request |
| `TkUnsupportedModelWireBody` / `TkUnsupportedModelWireFields` | Shared handler writers render the owner shape for marked requests (JSON and SSE) |
| `TkErrorWireForPath` / `ForRequest` / `ForShape` | Inbound path → wire; the only copy of this split (the group model allowlist writer maps its result) |
| `TkIsGatewayUnsupportedModel404(status, body)` | Recognizes this 404 coming back from a nested TokenKey upstream |
| `tkWriteAnthropicUnsupportedModel` | Service-layer Anthropic emit (upstream 404 translation, negative cache) |

Marking the request also registers the ops local error type
(`invalid_request_error` / `model_not_found`), so Gemini envelopes, which have no `type` field, stay classified as client-owned P3.

Guards:

- `gateway_service_tk_unsupported_model_contract_test.go` scans `service`, `handler`, and `server/middleware` production code. It fails if any file other than the owner composes `TkUnsupportedModelMessage` / `TkUnsupportedModelErrType`, hardcodes the `model_not_found` / `unsupported_model` wire code, or pairs `StatusBadRequest` with "Unsupported model".
- `scripts/sentinels/gateway-tk.json` pins the owner, the contract test, the wire-shape test, the nested-404 test, and each consumer hook.

## Official shapes

| Inbound protocol | Status | Body |
| --- | --- | --- |
| OpenAI (chat/completions, responses, embeddings, images, audio, video, models, input_tokens) | 404 | `{"error":{"message":"Unsupported model: X","type":"invalid_request_error","param":null,"code":"model_not_found"}}` |
| Anthropic (`/v1/messages`, `/v1/messages/count_tokens`) | 404 | `{"type":"error","error":{"type":"not_found_error","message":"Unsupported model: X"}}` |
| Gemini native (`/v1beta/...`) | 404 | `{"error":{"code":404,"message":"Unsupported model: X","status":"NOT_FOUND"}}` |

After an SSE stream has started, the status is already 200. In that case the same type and code travel in the in-stream error event (`response.failed` on `/v1/responses`).

Sources:

- OpenAI: the error object requires `type`, `message`, `param`, and `code` (`components/schemas/Error` in [openai-openapi](https://github.com/openai/openai-openapi/blob/master/openapi.yaml)). The SDKs map 404 to `NotFoundError` ([openai-python `_client.py` / `_exceptions.py`](https://github.com/openai/openai-python)). `model_not_found` with `invalid_request_error` is the code the API returns for an unknown model; DashScope's OpenAI-compatible mode reproduces it (fixture in `newapi_model_not_found_tk_test.go`). OpenAI's [error-codes guide](https://developers.openai.com/api/docs/guides/error-codes) does not list 404 explicitly.
- Anthropic: "404 - `not_found_error`: The requested resource was not found", with the envelope `{"type":"error","error":{"type":...,"message":...}}` ([Claude API errors](https://platform.claude.com/docs/en/api/errors)).
- Gemini / Google APIs: `NOT_FOUND` maps to HTTP 404, and the JSON body is `{"error":{"code":404,"message":...,"status":"NOT_FOUND"}}` ([AIP-193](https://google.aip.dev/193); [Gemini API errors](https://aistudio.google.com/docs/api-errors)).

## Classification

| Case | Status | Owner |
| --- | --- | --- |
| Unknown / unserved model name (`ErrUnsupportedModel`, `ErrUniversalUnsupportedModel`, Anthropic cross-vendor ingress, group negative cache, Anthropic upstream `not_found_error`, not-found negative cache) | **404** | this doc |
| Group has accounts but none map the model (`classifyNoAccountError` ModelNotFound) | **404** | this doc (status from owner; message is site-specific) |
| Group model allowlist rejects the model | **404** | this doc (status from owner; `OpenAIErrorWriter` body from owner) |
| `GET /v1/models/{id}` not in catalogue | **404** | this doc (`param:"model"`) |
| Unknown `gpt-image-*` id with no pricing row | **404** | this doc |
| Model exists, this request shape is not servable (`TkUnsupportedRequest*`, `ErrUniversalUnsupportedRequest`, protocol route unavailable) | 400 | unchanged |
| Unsupported input modality | 400 | unchanged |
| Retired / deprecated model with replacement hint (`ErrDeprecatedAnthropicModel`, `ErrDeprecatedOpenAIModel`) | 400 | unchanged (model is known, retirement is not "unknown") |
| Known but unpriced media model | 400 | unchanged |
| Empty pool / capacity / all rate-limited | 429 + Retry-After | unchanged (`tkNoAvailableAccounts`) |
| No entitled group / permission | 403 | unchanged |
| Upstream (non-TokenKey) 404 passthrough on the native OpenAI path | upstream status | unchanged (`tkWriteOpenAINativeClientError`) |

## Consumers

| Consumer | Behavior after change |
| --- | --- |
| Ops error classification (`ops_error_logger*.go`) | `markOpsClientRequestRejected` and `OpsClientBusinessLimitedReasonUnsupportedModel` still set → phase=request, owner=client, P3 on every wire (test `TestClassifyOpsUnsupportedModel404IsClientRequestOnEveryWire`) |
| Nested TokenKey upstream returning this 404 | `TkIsGatewayUnsupportedModel404`: capability failover (`isModelCapabilityFailure`, OpenAI-compat account fault); no `(account, model)` cooldown (`HandleUpstreamModelNotFound`); not written to the Anthropic not-found negative cache; on exhaustion answers the SSOT 404 instead of 502. Legacy nested 400 keeps its old handling. |
| `gateway_service_tk_model_notfound_cache.go` | Hit writes the SSOT 404 (Anthropic shape) |
| `gateway_service_tk_group_unsupported_cache.go` | Unchanged; it returns `ErrUnsupportedModel`, which the handler maps through the owner |
| Frontend `studioGatewayError.tk.ts` | Branches on message text, not status; it never matched this message, so there is no change |
| `ops/stage0/smoke_lib.sh` | Matches "Unsupported model:" by message for any non-2xx/5xx/429 status, so it covers 404 and the legacy 400 with the same soft-skip (chat) / hard-fail (messages) verdicts |
| `ops/stage0/post_deploy_smoke.sh` (gemini section) | Soft-skips 404 or 400 when the message starts with "Unsupported model:"; any other 404 stays a hard failure |
| `ops/stage0/probe_account_model_verdict.py` | Local "Unsupported model" on 400/404 → `gateway_rejected`, checked before the upstream `model_not_found` marker |
| `ops/pricing/probe-servable-models.sh` | Local "Unsupported model" on 400/404 → `inconclusive`, checked before the upstream not-found markers |

## Rollout

- Deployed pre-SSOT binaries (prod, cn-prod, any nested TokenKey stub) still answer 400 until they are upgraded. Every ops script above accepts both 400 and 404 by matching the message. Drop the 400 branch only after all fleets run this version.
- Clients that branch on HTTP 400 for this case see 404 instead. OpenAI/Anthropic/Google SDKs raise `NotFoundError` (or equivalent) rather than `BadRequestError`. Neither status is retried by the official SDKs.
- Upstream (Wei-Shaw/sub2api) files touched minimally: `no_account_error.go`, `openai_models_handler.go`, `middleware.go` (`OpenAIErrorWriter`), `group_model_allowlist.go`, `gateway_upstream_response.go`, `model_not_found_error.go`, `openai_gateway_upstream_errors.go`, and the shared writers in `gateway_handler*.go` / `openai_gateway_handler.go`. No upstream behavior is removed; the upstream 404 cases keep 404 and only take their status and shape from the owner.
