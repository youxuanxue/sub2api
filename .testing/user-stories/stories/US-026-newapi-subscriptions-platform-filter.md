# US-026-newapi-subscriptions-platform-filter

- ID: US-026
- Title: NewAPI 第五平台 round-5 audit 修复（admin SubscriptionsView 平台筛选下拉漏掉 newapi）
- Version: V1.0
- Priority: P1
- As a / I want / So that: 作为运维，我希望在
  `/admin/subscriptions` 页面用平台筛选下拉框过滤订阅时能选到第五平台
  `newapi`，以便在第五平台 group 下分发了订阅之后，能在订阅列表里直接
  按平台过滤定位到这些 newapi 订阅，而不是只能选 anthropic / openai /
  gemini / antigravity 四项再瞎找。后端 `subscription_handler.List` 一直
  接受 `platform=newapi` 查询参数（透传到 `user_subscription_repo.List`
  → `usersubscription.HasGroupWith(group.PlatformEQ(platform))`），唯独
  前端这一处下拉硬编码了 4 项，把第五平台从 UX 上隐藏了。
- Trace: 角色 × 能力（admin × 订阅筛选）+ 防御需求（前后端契约不能在
  UI 层单方面收窄）
- Risk Focus:
  - 逻辑错误：前端硬编码 4 平台列表，与 `usePlatformOptions`
    composable 维护的"5 平台单一事实源"漂移，未来再加第 6 平台时这一
    处也会继续掉队（漂移会复发）
  - 行为回归：替换为 `optionsWithAll(() => t(...))` 后，原有 4 平台
    选项必须保持一致（顺序、标签）；i18n key 不能因为复用 composable
    而变化（仍然指向 `admin.subscriptions.allPlatforms`）
  - 安全问题：不适用——本次只调整 UI 下拉项的可见集合，不放宽任何
    访问控制
- Round-1 (US-022) 修了 admin 平面 5 项缺口；round-2 (US-023) 修了
  runtime 3 项缺口；round-3 (US-024) 修了批量导入 + 卡片过滤；round-4
  (US-025) 修了粘性会话 self-heal；round-5 是 admin UI 最后一处硬编码
  4 平台 picker 收口——`usePlatformOptions` composable 自此覆盖每个
  admin filter / picker 的单一事实源。

## Acceptance Criteria

1. AC-001 (正向 / picker 含 newapi): Given 在
   `/admin/subscriptions` 挂载 `SubscriptionsView`，When 平台筛选 `Select`
   组件挂载完成，Then 它收到的 `options` 必须包含 `value: "newapi"` 一
   项，且 `label === "New API"`。
2. AC-002 (回归保护 / 选项集与顺序一致): Given 同样挂载流程，When 检
   查平台筛选 `Select` 的 `options`，Then 必须**精确**等于
   `["", "anthropic", "openai", "gemini", "antigravity", "newapi"]`，
   长度 6，sentinel `value: ""` 在首位（用 i18n
   `admin.subscriptions.allPlatforms` 标签）；防止未来重新引入硬编码
   或意外多/漏一项。
3. AC-003 (反向验证): Given 临时回退本次修复（恢复硬编码 4 平台数
   组），When 跑 AC-001 / AC-002，Then 必须 fail（`options.length === 5`，
   不含 `newapi`）；这是机械证明本测试真的在覆盖修复点而不是通过其它
   路径自带绿。
4. AC-004 (回归): Given 本次代码变更，When 执行
   `pnpm vitest run src/views/admin/__tests__/us026SubscriptionsViewPlatformFilter.spec.ts`，
   Then 全部通过。

## Assertions

- `platformFilterOptions[0] === { value: '', label: 'admin.subscriptions.allPlatforms' }`
- `platformFilterOptions.find(o => o.value === 'newapi')?.label === 'New API'`
- `platformFilterOptions.length === 6`
- `platformFilterOptions.map(o => o.value) === ['', 'anthropic', 'openai', 'gemini', 'antigravity', 'newapi']`
- 反向验证：`git stash` 撤回 SubscriptionsView 的修复，AC-001 / AC-002
  必须 fail（已本地验证：`expected length 5 to be 6`）

## Linked Tests

- `frontend/src/views/admin/__tests__/us026SubscriptionsViewPlatformFilter.spec.ts`::`includes newapi in the platform filter options (regression: hardcoded 4-platform list silently dropped fifth platform)` (AC-001)
- `frontend/src/views/admin/__tests__/us026SubscriptionsViewPlatformFilter.spec.ts`::`exposes exactly the 5 canonical platforms + sentinel "all" entry (no extras, no missing)` (AC-002)
- 运行命令: `pnpm vitest run src/views/admin/__tests__/us026SubscriptionsViewPlatformFilter.spec.ts`

## Evidence

- 2 个新单测通过（已本地验证）
- 反向验证：`git stash` 后两测 fail，`git stash pop` 后两测 pass
- 修改的代码位置：
  - `frontend/src/views/admin/SubscriptionsView.vue` 删除硬编码
    `platformFilterOptions = computed(() => [...4 platforms...])`，改为
    `const { optionsWithAll } = usePlatformOptions()` +
    `optionsWithAll(() => t('admin.subscriptions.allPlatforms'))`

## Out of Scope (Round-5 audit findings 但本次不修)

- 后端 `IsOpenAICompatPoolMember` 在 OpenAI compat 池路径上已经全部正
  确覆盖 newapi；本轮 audit 未发现新的服务端缺口。
- `GetFallbackModel` 仍只服务 Antigravity（与 4 平台 fallback 字段一
  致）——明确的设计选择，不是 newapi 缺口。
- 上游 newapi adaptor 的流式 `stream_options.include_usage=false` 行为
  属于上游契约，不是 sub2api fork 自己的分支问题。

## Status

- [x] Done
