# US-058 Studio 与 Quickstart 生图请求一致性

- ID: US-058
- Priority: P1
- Title: Studio 与 Quickstart 生图请求一致性
- As a / I want / So that: 作为 TokenKey 用户，我希望按当前密钥和模型选择合法生图参数，并复制与 Studio 一致的接入示例，避免路由或参数错误。
- Trace: `docs/approved/image-generation-quickstart-studio.md`
- Risk Focus:
  - 逻辑错误：GPT aspect_ratio 不与 size 混发，Gemini Web 使用获准比例，计费不决定请求协议。
  - 行为回归：Seedream size、文本接入、历史复用、模型和密钥切换保留正确行为。
  - 安全问题：Direct key 不借用其他分组能力，匿名预览不查询密钥，登录返回不携带凭据。
  - 运行时：密钥切换重新加载能力，迟到响应不替换当前密钥；验证密钥不发付费请求。

## Acceptance Criteria

1. AC-001（正向）：Universal Gemini、GPT 和专用 Gemini 示例使用合法路径和请求结构；Python 实际保存返回图片。
2. AC-002（负向）：Web-only 不暴露其他路径的比例；未获准分组不产生生图能力；未知能力不发送猜测的高级参数。
3. AC-003（回归）：切换密钥、模型、导航和历史恢复都归一化参数；GPT 不带 size；Seedream 保留像素映射。
4. AC-004（安全）：匿名只展示占位示例；生图密钥验证不发送生成请求；Studio 跳转携带模型与合法配置但不携带凭据或自动生成。

## Assertions

- AC-001：比较 Chat/native/Images 完整请求体；用隔离 Python fixture 执行示例并核对保存的文件字节。
- AC-002：Direct Web key 的比例集合与范围一致，拒绝的图片分组无配置，未知模型仅发送 model/prompt。
- AC-003：Playwright 选择、历史复用、密钥切换后的 aria-pressed 与实际提交一致。
- AC-004：浏览器与 fetch 记录中匿名无私有请求，密钥验证只有 GET，导航 URL 无 secret。

## Linked Tests

- AC-001/002: `backend/internal/service/candidate_image_capabilities_tk_test.go`::`TestImageCapabilityProfilesRespectKeyAndWebPath`
- AC-001/002: `backend/internal/service/candidate_image_capabilities_tk_test.go`::`TestImageCapabilityGPTUsesExistingRatioContract`
- Run command: `cd backend && go test -tags=unit ./internal/service -run '^TestImageCapability' -count=1`
- AC-001/003: `frontend/src/utils/__tests__/imageGeneration.tk.spec.ts` verifies exact request bodies, normalization and executes the Python saver with fixture transport.
- AC-004: `frontend/src/composables/__tests__/useTkUseKey.spec.ts` verifies image key checks perform GET only.
- Run command: `cd frontend && pnpm test:run src/utils/__tests__/imageGeneration.tk.spec.ts src/composables/__tests__/useTkUseKey.spec.ts src/components/keys/__tests__/UseKeyModal.spec.ts`
- AC-001/003/004: `frontend/e2e/image-generation-contract.e2e.ts` drives real Vue UI through model/key changes, generation, code examples, key verification and anonymous login navigation.
- Run command: `cd frontend && pnpm exec playwright test --config playwright.studio-surface.config.ts`

## Evidence

本地后端 service/handler 全套 unit、前端 Vitest 全套、类型检查、生产构建通过。
Playwright Studio 生图/历史复用/密钥切换、Quickstart 示例/验证密钥/缓存页面回跳、
BakeOff、匿名跳转与原有公开注册/登录旅程通过。截图在 frontend/e2e/artifacts/，
生产构建在 backend/internal/web/dist/。
API 夹具浏览器验收不声称真实上游生成或最终输出尺寸已验证；没有部署。

## Status

- Done
