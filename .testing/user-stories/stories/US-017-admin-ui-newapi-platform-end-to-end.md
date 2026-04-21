# US-017-admin-ui-newapi-platform-end-to-end

- ID: US-017
- Title: Admin UI 接入第五平台 newapi（端到端可创建组与账号）
- Version: V1.2
- Priority: P0
- As a / I want / So that: 作为 sub2api 管理员，我希望在 Admin UI（创建组弹窗、创建账号弹窗、平台过滤器、账号列表 PlatformTypeBadge）里都能看到 `newapi`，并能完成"新建 newapi 组 → 新建 newapi 账号 → 账号正确归类"的端到端流程，以便已经 ship 的第五平台后端能力（`docs/approved/newapi-as-fifth-platform.md`）真正可被使用，而不是只能通过 `psql`/curl。
- Trace: design `docs/approved/admin-ui-newapi-platform-end-to-end.md`（来源：角色×能力，admin × 创建账号/组）+ `docs/approved/newapi-as-fifth-platform.md` §"被推迟的工作 — Admin UI 集成"
- Risk Focus:
  - 逻辑错误：CreateAccount 未携带 top-level `channel_type` 时被后端 `admin_service.go:1565` 拒绝（`channel_type must be > 0 for newapi platform`）。
  - 行为回归：`PlatformTypeBadge.vue` 历史 default 把任意未知 platform 渲染成 "Gemini"——`newapi` 账号必须显示 "New API" 而非 "Gemini"。
  - 行为回归：原有 4 个平台（anthropic/openai/gemini/antigravity）的下拉、过滤、徽章不能因新增 `newapi` 选项而错位/重排序；canonical 顺序由 `GATEWAY_PLATFORMS` 单一来源决定。
  - 安全问题：不适用（新增 UI 选项不放宽鉴权；后端 `CreateGroup`/`CreateAccount` 路由本来就受 admin 中间件保护）。

## Acceptance Criteria

1. AC-001 (正向 / 单一事实)：Given canonical `GATEWAY_PLATFORMS = ['anthropic','openai','gemini','antigravity','newapi']`，When `usePlatformOptions()` 渲染，Then 返回 5 项且第 5 项是 `{value:'newapi', label:'New API'}`，顺序与 `GATEWAY_PLATFORMS` 一致 (`TestUS017_PlatformOptions_AllFiveCanonicalOrder`).
2. AC-002 (正向 / GroupsView 过滤器)：Given `optionsWithAll('admin.groups.allPlatforms')` 被 GroupsView 调用，When 渲染过滤器，Then 第一项为 `{value:'', label:<allLabel>}`，剩余 5 项与 `GATEWAY_PLATFORMS` 一致 (`TestUS017_PlatformFilter_PrependsAllOption`).
3. AC-003 (负向 / Badge fallback)：Given 一个 platform 字符串 `'newapi'` 传入 `PlatformTypeBadge`，When 渲染，Then label === 'New API'（不是 'Gemini'）且 platformClass 含 `bg-cyan-100`。Given 真正未知 platform `'unknown-x'`，Then label === `'unknown-x'` 且 platformClass 走中性灰 fallback（不再被错标 Gemini）。
4. AC-004 (回归 / 4 个老平台 Badge 不变)：Given 历史平台 `anthropic`/`openai`/`gemini`/`antigravity`，When 渲染 PlatformTypeBadge，Then label 与 class 与升级前快照完全一致（颜色：橙/绿/蓝/紫）。
5. AC-005 (回归 / CreateAccountModal 4 个老平台分支不变)：Given 用户选 `anthropic`/`openai`/`gemini`/`antigravity`，When 触发 OAuth/apikey/bedrock/upstream 各子流程的 `handleSubmit`，Then 走原有分支，`isOAuthFlow` 不被 newapi 改动影响。

## Assertions

- AC-001/AC-002：composable spec 中 `expect(options.value.map(o => o.value)).toEqual(GATEWAY_PLATFORMS)` 与 `expect(filterOptions.value[0].value).toBe('')`。
- AC-003：组件挂载断言 `wrapper.text()` 含 `'New API'` 且不含 `'Gemini'`；`wrapper.html()` 含 `bg-cyan-100`；fallback case 断言含 `bg-gray-100`。
- AC-004：4 个平台分别快照 `platformLabel` + `platformClass` 字符串。
- AC-005：CreateAccountModal `isOAuthFlow` 在 platform=anthropic+oauth-based 时为 true，在 platform=newapi 时为 false（防回归）。
- 失败时 vitest `expect` 立即报错并 exit ≠ 0。

## Linked Tests

Vitest specs (test names are descriptive in JS-land — convention `it('...')` not
`TestUSXXX_*`; story IDs are referenced in the `describe(...)` block instead):

- `frontend/src/composables/__tests__/usePlatformOptions.spec.ts`::`exposes exactly the 5 canonical gateway platforms in GATEWAY_PLATFORMS order` *(AC-001)*
- `frontend/src/composables/__tests__/usePlatformOptions.spec.ts`::`includes newapi (the bug we are fixing — admin pickers used to drop it)` *(AC-001)*
- `frontend/src/composables/__tests__/usePlatformOptions.spec.ts`::`optionsWithAll prepends the localized "all" sentinel and preserves order` *(AC-002)*
- `frontend/src/components/common/__tests__/PlatformTypeBadge.spec.ts`::`renders newapi as "New API" with cyan styling (the bug we are fixing)` *(AC-003)*
- `frontend/src/components/common/__tests__/PlatformTypeBadge.spec.ts`::`NEGATIVE — truly unknown platforms fall back to neutral gray (no silent Gemini mislabel)` *(AC-003 negative)*
- `frontend/src/components/common/__tests__/PlatformTypeBadge.spec.ts`::`REGRESSION — the 4 historical platforms render with their canonical brand label and color` *(AC-004)*
- 运行命令: `cd frontend && pnpm test:run usePlatformOptions PlatformTypeBadge`

AC-005 (CreateAccountModal `isOAuthFlow` regression) is currently asserted by
manual Stage-4 smoke-test in the PR description; converting it to a vitest spec
requires mounting the full modal (heavy fixtures) and is deferred until the
prototype is approved and we move to feature implementation.

## Evidence

- `.testing/user-stories/attachments/us-017-admin-ui-newapi-vitest-2026-04-20.txt`（待 stage 4 测试运行后归档）

## Status

- [ ] Draft (prototype scope: composable + GroupsView + CreateAccountModal + Badge wired; vitest specs pending stage 4)
