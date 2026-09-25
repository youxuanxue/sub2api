# US-057 Gemini Web 兼容入口复用原生协议

- ID: US-057
- Priority: P1
- Title: 区分普通 Gemini 与受限 Web，通过共享 converter 承接兼容协议
- As a / I want / So that: As an API client, I want Gemini-compatible requests to use the selected provider through one native transport, so that generated images survive protocol conversion without losing client intent.
- Trace: docs/approved/gemini-web-channel.md 的兼容协议推进计划；本轮用户授权 Agent 并行实现。
- Risk Focus:
  - 逻辑错误：能力与传输混淆、转换器虚构默认值、图片遗漏或重复计数。
  - 行为回归：普通 Gemini 多轮/system、原生生图和既有 AG/Vertex 路径受影响。
  - 安全问题：Web 不支持的语义被删减、候选回退越过授权、能力变化后旧 Plan 被执行。
  - 运行时问题：SSE 累积图片重复、图片早于最终 usage 丢失、断流伪造完成、无效图片计费。

## Acceptance Criteria

1. AC-001 (positive): Given compatible native/Messages/Chat/Responses single-turn Web requests When Plan selects and transport forwards Then native generateContent receives the mapped model, API-key auth and exact image options, and client output retains the image.
2. AC-002 (negative): Given system/history/tools/media input or malformed output limits When Web is considered Then Plan rejects before transport; an authorized capable generic Gemini peer remains eligible.
3. AC-003 (regression): Given a generic Gemini API-key account When a valid Messages request includes max_tokens Then the limit reaches native generationConfig and image output survives; Web preserves the original request digest and uses an audited best-effort effective request without this limit, according to the user-approved contract.
4. AC-004 (security): Given an image-only model, restricted direct key or changed capability identity When selecting/rechecking Then image models retain Plan, fallback stays authorized and stale/conflicted linked evidence is rejected.
5. AC-005 (runtime): Given repeated/independent image SSE events or a later text-only final event When converting Then each image is preserved according to its multiplicity, actual outputs alone are counted, and malformed or truncated streams never claim successful completion.

## Assertions

AC-001/003: assert actual HTTP method/path/header, mapped model, user prompt, generationConfig and legal response image data through Plan → execution recheck → real converter → HTTP fixture.
AC-002: assert Plan rejection and selected capable peer, not merely validator existence; Messages required max_tokens is validated on the original request; Web budget adaptation is explicit in Plan, while generic providers retain the limit.
AC-004: assert selected Plan, account/billing scope, changed provider capability key and authoritative linked evidence failures.
AC-005: assert emitted image deltas, final envelope semantics, observed counts, malformed-output error and preserved early images.

## Linked Tests

- `backend/internal/service/us057_gemini_web_protocol_test.go`::`TestUS057_GeminiNativeProtocolTransport`
- `backend/internal/service/us057_gemini_web_protocol_test.go`::`TestUS057_GeminiImageCandidateKeepsSelectedPlan`
- `backend/internal/handler/us057_gemini_partial_usage_test.go`::`TestUS057_GeminiCompatIngressMetersDeliveredPartialImageOnce`
- `backend/internal/engine/protocolrouter/gemini_web_test.go`::`TestGeminiWebPlanSingleTurnAndGeneralProviderRegression`
- `backend/internal/service/protocol_gemini_native_contract_test.go`::`TestGeminiNativeDeclaredContractDoesNotInventProbeEvidence`
- `backend/internal/service/protocol_gemini_native_contract_test.go`::`TestGeminiNativeMissingPersistedDeclarationFailsClosed`
- `backend/internal/service/protocol_gemini_native_contract_test.go`::`TestGeminiWebProviderIdentityInvalidatesLinkedGeneralCapability`
- `backend/internal/service/protocol_gemini_native_contract_test.go`::`TestGeminiWebNativeImageRejectsHistoryBeforeSelection`
- `backend/internal/service/protocol_gemini_native_contract_test.go`::`TestGeminiWebOriginalIntentFallsBackToGeneralGemini`
- `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidateRejectsProductionChatWithoutPoisoningPeer`
- `backend/internal/service/gemini_web_request_tk_test.go`::`TestGeminiWebCandidateRechecksCapabilityAfterWait`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestGeminiCompatStreamsPreserveImagesOnce`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestGeminiCompatNonStreamingMalformedImageFails`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestCollectGeminiSSERetainsEarlierImages`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestGeminiCompatStreamsRejectIncompleteOrMalformedEvents`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestCollectGeminiSSERejectsMalformedImagesBeforeAggregation`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestGeminiCompatForwardPreservesInterruptedStreamUsage`
- `backend/internal/service/gemini_compat_image_options_test.go`::`TestGeminiCompatForwardBufferedTruncationDoesNotBillUnreturnedImage`
- `backend/internal/repository/protocol_endpoint_capability_repo_test.go`::`TestGeminiNativeDeclarationSeedsOnlyNewCapabilityAndPreservesProbeDenial`

Run command: `cd backend && go test -tags=unit ./internal/engine/protocolrouter ./internal/service ./internal/handler -run 'Test(US057|GeminiWeb|GeminiNativeDeclared|GeminiCompat|CollectGeminiSSE|ObserveGeminiImage|ResolveGeminiImage)' -count=1`

- Additional budget regressions: `TestGeminiWebBestEffortBudgetsUseImmutableAuditedPlan`, `TestGeminiWebBestEffortDoesNotAdmitInvalidBudgetsOrOtherControls`, and `TestGeminiWebBestEffortPrefersExactGenericPeer`.
- Policy-terminal regression: `backend/internal/service/gemini_compat_image_options_test.go`::`TestGeminiCompatForwardPolicyBlockIsTerminal` and `TestGeminiForwardNativeBufferedSafetyEOFIsRequestScoped`.

## Evidence

Initial clean-main preflight passed. Focused execution matrix and AG/Vertex regressions passed locally.
Engine, apicompat, service, handler and repository unit suites passed locally. After the final best-effort and policy-terminal fixes, the complete service suite (144.144s), engine suite and focused Web/native/US057 forwarding tests passed again. The handler full suite passed after partial-usage settlement changes; the strengthened eligible-peer no-replay regression also passed. Probe tooling tests passed with Pillow available.
Final preflight evidence is recorded with the delivered change; production validation remains separate.
HTTP transport fixtures are integration tests, not UI e2e and not production provider evidence.
Production canary is pending deployment; no release, capability refresh or account mutation has been performed.
The user explicitly approved Web best-effort output limits. All three compatibility ingresses must pass the corresponding transport tests; production success still requires deployment and account-attributed canaries.

## Status

- InTest
