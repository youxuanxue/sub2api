# US-059 Antigravity OpenAI Images 协议适配

- ID: US-059
- Priority: P1
- Title: Antigravity OpenAI Images 协议适配
- As a / I want / So that: 作为有图片权限的 API 用户，我希望通过 Images 接口使用 Antigravity OAuth 生图，以便现有 OpenAI Images 客户端直接读取图片。
- Trace: `docs/approved/antigravity-images-generations-adapter.md`；用户授权“补一层协议。继续。”
- Risk Focus:
  - 逻辑错误：尺寸/比例丢失、默认 2K 与计费不一致、转换失败仍扣费。
  - 行为回归：影响原生 Gemini、兼容协议和其他 Images 供应商路由。
  - 安全问题：Direct 借用其他组、图片权限关闭仍执行、无原生 capability 绕过 Plan。
  - 运行时问题：坏图/未完成输出重放、重复计费、缓冲无界增长。

## Acceptance Criteria

1. AC-001（positive）：Given 有效 AG OAuth 或 edge relay 账号 When 调用 Images Then 生成相同参数的原生 Gemini 请求，返回可解码 b64_json，并记录一次原入口用量。
2. AC-002（negative）：Given n>1、url、stream 或冲突尺寸 When 接入 Then 返回 400，不绑定账号；Given 坏图/多图/空图/未完成输出 Then 返回 502，不扣费、不重放。
3. AC-003（security）：Given 无权组、图片禁用或 capability 不合法 When 候选准入 Then 拒绝；Direct/Universal 均使用真实 AG 账号的原生 Plan，不根据计费组平台选择 handler。
4. AC-004（regression）：Given 既有文本/图片协议或 OpenAI/Grok/newapi Images When 执行相关回归 Then 保持合法路由和响应。
5. AC-005（runtime）：Given 响应大于 32 MiB When facade 缓冲 Then 终止缓冲并返回 502，未将原生内容泄露给 Images 客户端。

## Assertions

- AC-001：图片与 STOP 分事件、随后 usage-only 事件仍返回成功并只计量一次；比较上游 request 中 imageSize/aspectRatio；PNG 解码返回值；核对 usage account、ImageCount、ImageSize 和 InboundEndpoint。
- AC-002：独立 SAFETY 结束事件仍拒绝；核对 HTTP 状态、上游调用次数、usage 为空与 key.GroupID 未绑定。
- AC-003：核对不可变 request digest、原生 AdapterID、选中账号及拒绝结果。
- AC-004：混合 AG/newapi 池分派后仅重选同协议账号，耗尽时不向另一协议供应商错发请求；运行既有原生/兼容/候选/路由回归，不修改其期望。
- AC-005：实际写入超限字节，断言 ErrShortBuffer、内部/客户端缓冲为空和最终 502。

## Linked Tests

- AC-001/002: `backend/internal/handler/us059_gemini_images_ingress_test.go`::`TestUS059_GeminiImagesIngressGenerationAndSettlement`
- AC-002: `backend/internal/pkg/apicompat/gemini_images_test.go`::`TestImagesToGeminiGenerationRejectsLossyOptions`
- AC-002: `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestGeminiImagesInvalidOptionsRejectBeforeCandidateBinding`
- AC-003: `backend/internal/service/gemini_images_adapter_tk_test.go`::`TestGeminiImagesCandidateUsesNativePlan`
- AC-003: `backend/internal/service/gemini_images_adapter_tk_test.go`::`TestGeminiImagesCandidateRejectsUnauthorizedOrLossyRequests`
- AC-004: `backend/internal/server/routes/gateway_tk_openai_compat_image_dispatch_test.go`::`TestTkOpenAIImageGenerationsDispatch_CompatPoolUsesImageGenerations`
- AC-004: `backend/internal/service/gemini_images_adapter_tk_test.go`::`TestGeminiImagesPreservesExistingNewAPIProvider`
- AC-004: `backend/internal/service/gemini_images_adapter_tk_test.go`::`TestGeminiImagesSelectionStaysWithDispatchedProtocol`
- AC-005: `backend/internal/handler/gemini_images_tk_test.go`::`TestGeminiImagesWriterEnforcesBufferLimit`

Run command: `cd backend && go test -p 2 -tags=unit ./internal/pkg/apicompat ./internal/engine/protocolrouter ./internal/service ./internal/handler ./internal/server/middleware ./internal/server/routes -run 'Test(ImagesToGemini|GeminiGenerationToImages|Gemini|Antigravity|WrapNativeGeminiRequest|Candidate|GlobalCandidate|Universal|Protocol|TkOpenAI|US057_Gemini|US059|ImageCapability)' -count=1`

## Evidence

完整 handler 的 OAuth/relay fixture 验证通过；候选与转换器回归通过。
使用本地 HTTPUpstream fixture，未部署、未向真实 Google 上游发送此新入口请求。
这是纯 API 适配，不修改 UI 页面；fixture 集成测试不称作浏览器 e2e。

## Status

- InTest（本地实现；线上验收待部署）
