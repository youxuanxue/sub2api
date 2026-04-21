---
title: Admin UI — 第五平台 `newapi` 端到端可见性与可操作性
status: pending
approved_by: pending
approved_at: pending
authors: [agent]
created: 2026-04-20
related_prs: []
related_commits: []
related_stories: [US-018]
related_audit: tester report 2026-04-20 — 创建分组 modal 缺失第五平台选项
supersedes: none
parent_design: docs/approved/newapi-as-fifth-platform.md
---

# Admin UI — 第五平台 `newapi` 端到端

## 0. TL;DR

`docs/approved/newapi-as-fifth-platform.md`（v1.4.0 已 ship）明确**推迟了 admin UI 集成**
（"frontend：`platformOptions` 是否含 newapi 由 admin UI 决定，不在本 design 范围"）。
今天的现状：后端、调度器、sticky routing、错误透传、bridge —— 所有运行时路径都把
`newapi` 当作一等的第五平台；**但 admin UI 完全不暴露它**。运维无法通过界面创建
newapi 分组或账号，唯一的绕路是手工构造 admin API 调用。

本设计用**最小**的 UI 改动闭合这一缺口，让运维可以端到端跑通 newapi
（创建分组 → 创建账号 → 看到正确标注的账号 → 列表筛选）。Out-of-scope 的精修
（ops-dashboard 筛选、错误透传规则、批量编辑）会列入 stage-3 跟进，但明确排除
在本次原型之外。

PR #19 review 后，§1.5「审批前补强」额外把 `ChannelsView.vue` 修进来——它的
4 元素 `platformOrder` 不只是视觉漂移，而是一个**会静默吞掉 newapi channel
数据的 bug**（详见 §1.5）。同时 `utils/platformColors.ts` 的 cyan 完整化和
i18n 的 `newapi` 标签也一并补齐。

## 1. 范围

### In-scope（本设计 + 原型）

1. **平台选项的单一事实源** —— 抽出 `usePlatformOptions()` composable，
  背后由 `frontend/src/constants/gatewayPlatforms.ts` 的 `GATEWAY_PLATFORMS`
   驱动（其中已经包含 `newapi`）。替换 `GroupsView.vue` 中两处硬编码的选项列表。
2. **创建账号** —— `CreateAccountModal.vue` 增加第 5 个平台 segment `newapi`，
  把已存在但未启用的 `AccountNewApiPlatformFields.vue` 接到已存在但未启用的
   `listChannelTypes()` / `fetchUpstreamModels()` API client 上。
3. **账号展示正确性** —— `PlatformTypeBadge.vue` 增加 `newapi` 分支，并停止
  把 "Gemini" 当作通配回退。（今天 newapi 账号在列表里会被渲染成
   "Gemini" + 蓝色徽章，这是数据展示静默错误，不是样式 nit。）
4. **回归保护** —— 用 **2 个** vitest spec 覆盖核心改动：
  - `usePlatformOptions.spec.ts`：断言返回规范顺序的 5 个平台，避免后续重构再次把 newapi 漏掉；
  - `PlatformTypeBadge.spec.ts`：断言 `newapi` 渲染为 "New API" + 青色，
  真未知平台走中性灰回退（不再被静默错标为 Gemini），4 个老平台快照不变。

### Out-of-scope（stage-3 backlog，原型审批合并后单独 PR 做）

> 设计阶段曾设想把 backlog 单独抽到 `docs/task-breakdown-admin-ui-newapi.md`，
> 该文件**最终未落地**（避免散文档漂移）。stage-3 任务列表就以本节为单一来源，
> follow-up PR 直接引用本节锚点即可。
>
> **2026-04-20 更新**：根据用户指令，原 Out-of-scope 中的下列条目已在 §1.6
> 全量并入 PR #19。仅剩 §6 列出的真正延后项（防漂移 preflight 段落需要
> 在 §1-§5 切完后单独落地）。

- ~~`AccountTableFilters.vue` / `OpsDashboardHeader.vue` / `ErrorPassthroughRulesModal.vue`
的平台 picker~~ → §1.6 #9-#11 已并入 PR #19。
- ~~`EditAccountModal.vue`~~ → §1.6 #8 已并入 PR #19；`BulkEditAccountModal.vue` 自动通过
`useModelWhitelist` 扩展（§1.6 #13）覆盖 newapi 模型映射，仅"批量改 `channel_type` 守卫"
仍属 stage-3（见 §6）。
- ~~`PlatformIcon.vue`~~ → §1.6 #20 已为 newapi 加专属 SVG 图标。
- ~~`SubscriptionsView.vue`~~ → §1.6 #18 已加 cyan accent dot；newapi 订阅模型由后端决定
渲染什么（OAuth 订阅概念不强加在 newapi 上，但平台标签必须正确显示）。

### 1.5 审批前补强（PR #19 review 后追加）

PR #19 的二次 review 对 `ChannelsView.vue` 做深度审计后，发现原 §1 Out-of-scope
中的两项不能延后到 stage-3 —— 它们触发了一个**已经在生产分支里的潜伏数据丢失
bug**：

- `ChannelsView.vue:721` 的 `platformOrder = ['anthropic','openai','gemini','antigravity']`
被两条**互不相干的代码路径**消费：
① `apiToForm` 第 1070 行用它过滤 `channel.model_mapping` 的键；
② `formToAPI` 第 1007 行迭代 `form.platforms`（其内容由 ① 决定）。
这意味着：**任何后端返回了 `newapi` 数据的 channel，被运维在 ChannelsView
打开并保存后，会静默丢失全部 `newapi` 行**（model_mapping、model_pricing、
group 关联）。
后端早就接受 `newapi` channel（`channel_handler_tk_newapi_admin.go:35`
在 `oneof` 白名单中显式列出 `newapi`，`channel_repo.go:133` 的 Update 全量
替换 JSONB），所以这不是"只读视图"假设的延伸，而是**前端 vs 后端之间的不
对称组织漂移**。
- `utils/platformColors.ts` 的 `Platform` 联合类型缺 `newapi`，导致 ChannelsView
在切到 composable 后，`newapi` 平台行/徽章的颜色会回退到默认灰，与设计承诺
的 cyan 不一致——不算数据 bug，但一旦 ChannelsView 开始渲染 `newapi`
必须同步修，否则视觉漂移。

补强清单（commits 4-N，与 §3 in-scope 共享同一个 PR #19）：


| #   | 路径                                                                 | 变更                                                                                                                                                                       | 风险归属        |
| --- | ------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------- |
| 1   | `frontend/src/utils/channelFormConversion.ts` (NEW)                | 抽出纯函数 `apiToFormSections` / `formSectionsToApi`，把 canonical platform order 当参数传入（默认 `GATEWAY_PLATFORMS`）；让 round-trip 可被单测覆盖                                             | 逻辑错误（数据丢失）  |
| 2   | `frontend/src/views/admin/ChannelsView.vue`                        | 删除本地 `platformOrder` 4 元素字面量，改 `import { GATEWAY_PLATFORMS }`；`apiToForm` / `formToAPI` 改为调用 §1 的纯函数                                                                     | 逻辑错误 + 行为回归 |
| 3   | `frontend/src/utils/platformColors.ts`                             | `Platform` 联合类型加 `'newapi'`；9 张 variant map 全部加 cyan 项；`isPlatform()` 与 `platformLabel()` 同步加分支；`Record<Platform, …>` 让漏一项编译失败                                           | 行为回归（视觉）    |
| 4   | `frontend/src/components/admin/channel/types.ts`                   | `getPlatformTagClass()` 增加 `case 'newapi'`（cyan tag）                                                                                                                     | 行为回归（视觉）    |
| 5   | `frontend/src/i18n/locales/{en,zh}.ts`                             | `admin.groups.platforms.newapi` 与 `admin.accounts.platforms.newapi` 都加 `'New API'`，避免 ChannelsView/GroupsView 显示原始 key                                                   | 行为回归（文案）    |
| 6   | `frontend/src/utils/__tests__/channelFormConversion.spec.ts` (NEW) | 9 个 vitest case：5 平台 round-trip 保留 newapi、用 4 元素旧 order 调用证明数据丢失（NEGATIVE）、纯 4 平台回归、`web_search_emulation` on/off/clear、disabled section 跳过、canonical order、空 pricing 过滤 | 防漂移护栏       |


`PlatformIcon` / `PlatformTypeBadge` 的 cyan 与 newapi 显示在 §1 已涵盖，
不在本节重复。

### 1.6 PR #19 全量收束 —— stage-3 提前合并

PR #19 第三轮 deep review（"查缺补漏 NewAPI 的能力，含用户端和管理端"）后，
用户决定**把原本拆到 stage-3 的 follow-up 全部并入 PR #19**，让 newapi
作为第五平台一次性达到与 openai 完全平价的端到端能力。原 §1 Out-of-scope
全部上调为 In-scope（除 §5.7 防漂移 preflight 段落保留为 stage-3，因为
要等 §1-§5 切完才不会 fail 自己）。

补强清单（commits N+1..M，与 §3 / §1.5 共享同一个 PR #19）：

**后端 P1（运行时正确性）**


| #   | 路径                                                   | 变更                                                                                                                      | 风险归属 |
| --- | ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- | ---- |
| 1   | `backend/internal/handler/gateway_handler.go`        | `/v1/models` 兜底响应在 fallback 路径下使用 API key group 的 `Platform`（含 `newapi`），不再硬编码 `"openai"`                               | 逻辑错误 |
| 2   | `backend/internal/service/openai_gateway_service.go` | `handleErrorResponse` / `applyErrorPassthroughRule` 用 `account.Platform`（含 `newapi`）做规则匹配，使运维可以为 newapi 配置专属错误透传规则      | 逻辑错误 |
| 3   | `backend/internal/handler/openai_gateway_handler.go` | `handleFailoverExhausted` 把真实平台（API key group 的 `Platform`）传给 `MatchRule`，避免 newapi failover 错误被错配为 openai 规则           | 逻辑错误 |
| 4   | `backend/internal/handler/ops_error_logger.go`       | `guessPlatformFromPath` 扩展 OpenAI-compat 启发式（`/v1/models` 等共享路径走"未知"标签），避免 newapi 错误日志被误归类                              | 逻辑错误 |
| 5   | `backend/internal/service/account_tk_compat_pool.go` | 把私有 `isOpenAICompatPlatform()` 提升为公开 `IsOpenAICompatPlatform`，供 §1.6 #1-#3 与 `openai_messages_dispatch_tk_newapi.go` 复用 | 防漂移  |


**前端 P1（端到端可见性）**


| #   | 路径                                                               | 变更                                                                                                                | 风险归属   |
| --- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- | ------ |
| 6   | `frontend/src/components/keys/UseKeyModal.vue`                   | 复制密钥 modal 的 tabs / 描述 / 文件名 / openCode deeplink 按 OpenAI-compat 处理 newapi（沿用 OpenAI 协议提示）                        | 行为回归   |
| 7   | `frontend/src/views/user/KeysView.vue`                           | CCSwitch deeplink 在 newapi 下与 openai 等价（OpenAI-compat HTTP 形态）                                                    | 行为回归   |
| 8   | `frontend/src/components/account/EditAccountModal.vue`           | 把 `CreateAccountModal` 的 newapi 分支端口过来（v-if + AccountNewApiPlatformFields 接线 + isOAuthFlow bypass），让 newapi 账号可编辑 | 行为回归   |
| 9   | `frontend/src/components/admin/account/AccountTableFilters.vue`  | 平台筛选切到 `usePlatformOptions().optionsWithAll(allLabel)`（响应式 i18n）                                                  | 防漂移    |
| 10  | `frontend/src/views/admin/ops/components/OpsDashboardHeader.vue` | 同上                                                                                                                | 防漂移    |
| 11  | `frontend/src/components/admin/ErrorPassthroughRulesModal.vue`   | 同上                                                                                                                | 防漂移    |
| 12  | `frontend/src/types/index.ts`                                    | `Account` 接口补 `channel_type?: number`（让 `EditAccountModal` 的 newapi 编辑路径在 TS 层面合法）                                | TS 完整性 |


**前端 stage-3 提前合并（端到端能力平价）**


| #   | 路径                                                               | 变更                                                                                                                                                                                        | 风险归属  |
| --- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- |
| 13  | `frontend/src/composables/useModelWhitelist.ts`                  | `getModelsByPlatform('newapi') === openaiModels`；`getPresetMappingsByPlatform('newapi') === openaiPresetMappings`。批量编辑 / 模型白名单选择器自动覆盖 newapi（避免单独维护一套 newapi 预设立刻漂移于 openai）              | 防漂移   |
| 14  | `frontend/src/composables/__tests__/useModelWhitelist.spec.ts`   | + 2 个回归 case：newapi 模型列表 / 预设映射与 openai 完全一致                                                                                                                                              | 防漂移护栏 |
| 15  | `frontend/src/views/admin/GroupsView.vue`                        | "Messages 调度配置" 的 v-if 由 `platform === 'openai'` 切到 `isOpenAICompatPlatform(form.platform)`（含 newapi）；账号过滤区扩展到 newapi，但 `require_oauth_only` / `require_privacy_set` 对 newapi 隐藏（无 OAuth） | 行为回归  |
| 16  | `backend/internal/service/openai_messages_dispatch_tk_newapi.go` | 内部 `isOpenAICompatPlatformGroup` 改为调用新的 `IsOpenAICompatPlatform`（去重）                                                                                                                      | 防漂移   |


**视觉 P2 / P3（newapi 颜色 + 图标）**


| #   | 路径                                                                  | 变更                                                                                                             | 风险归属 |
| --- | ------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- | ---- |
| 17  | `frontend/src/components/admin/group/GroupRateMultipliersModal.vue` | `platformColorClass` + `case 'newapi': cyan`                                                                   | 视觉回归 |
| 18  | `frontend/src/views/user/SubscriptionsView.vue`                     | `platformAccentDotClass` + `case 'newapi': bg-cyan-500`                                                        | 视觉回归 |
| 19  | `frontend/src/constants/gatewayPlatforms.ts`                        | NEW —— `OPENAI_COMPAT_PLATFORMS` 常量 + `isOpenAICompatPlatform()` helper（前端 canonical predicate，与后端 §1.6 #5 镜像） | 防漂移  |
| 20  | `frontend/src/components/common/PlatformIcon.vue`                   | NEW —— newapi 专属 SVG 图标（4 个节点 + 连线，relay/网络拓扑感，区别于 4 个上游品牌 mark）                                               | 视觉补全 |
| 21  | `frontend/src/views/HomeView.vue`                                   | "Supported Providers" 区块新增 New API 卡片（cyan 渐变 + Supported 标签）                                                  | 视觉补全 |
| 22  | `frontend/src/i18n/locales/{en,zh}.ts`                              | + `home.providers.newapi: 'New API'`                                                                           | 文案   |


不动 Ent / Wire / 数据库迁移。不引入新依赖。

### Non-goals（不会做，并解释为什么）

- **不新增 backend endpoint。** 后端 `CreateGroup` / `CreateAccount` 已经接受
`Platform: "newapi"`（admin_service.go:1565 强制 `channel_type > 0`）。
完全不需要后端改动；额外加一个会违反 CLAUDE.md §5 的最小 API surface 原则。
- **不新增协议级 DTO。** `AccountNewApiPlatformFields` 已经绑定到现有的
`channel_type` / `base_url` / `api_key` —— 字段通过现有 API 自然往返。
本设计仅在前端 TypeScript 类型 `CreateAccountRequest` 上把已经事实存在的
顶层 `channel_type?: number` 显式声明出来（见 §3.5 C3），不动后端 DTO。
- **不重构全局 UI。** 按 CLAUDE.md §5.x，应优先选择附加式注入而不是重写
upstream-shaped 文件（`CreateAccountModal.vue` 来自上游）。所有改动都是在
现有 `v-if` 链里追加。

## 2. 当前失败路径

```
用户 → 「分组管理」→ 创建分组
   → modal 平台下拉只渲染 [Anthropic, OpenAI, Gemini, Antigravity]
   → 用户无法选 newapi → 只能放弃或操作 admin API
```

根因：`frontend/src/views/admin/GroupsView.vue:2813-2818` 把 4 个元素的
`platformOptions` 字面量硬编码进去。同样的反模式在 `:2820-2826`（筛选器）以及
另外 4 个 admin view（`AccountTableFilters.vue`、`OpsDashboardHeader.vue`、
`ErrorPassthroughRulesModal.vue`、`SubscriptionsView.vue`）和 1 个展示组件
（`PlatformTypeBadge.vue`）里反复出现。每一处都是在 `GATEWAY_PLATFORMS` 常量
诞生（2026-04-19）之前写的，从未被重构去消费它。

这是**组织漂移**，不是逻辑 bug —— 规范的 `GATEWAY_PLATFORMS` 已存在；只是没有
任何地方用它来生成选项列表。

## 3. 设计

### 3.1 单一事实源 —— `usePlatformOptions()`

新增 `frontend/src/composables/usePlatformOptions.ts`：

```ts
import { computed } from 'vue'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AccountPlatform } from '@/types'

const PLATFORM_LABELS: Record<AccountPlatform, string> = {
  anthropic:  'Anthropic',
  openai:     'OpenAI',
  gemini:     'Gemini',
  antigravity:'Antigravity',
  newapi:     'New API',
}

export interface PlatformOption {
  value: AccountPlatform
  label: string
}

/** 规范的平台选项，顺序与 GATEWAY_PLATFORMS 一致。 */
export function usePlatformOptions() {
  const options = computed<PlatformOption[]>(() =>
    GATEWAY_PLATFORMS.map(p => ({ value: p, label: PLATFORM_LABELS[p] })))

  /** 筛选变体 —— 在调用点传入本地化的 "全部" 标签作为前置 sentinel。 */
  const optionsWithAll = (allLabel: string) =>
    computed<Array<{ value: '' | AccountPlatform; label: string }>>(() => [
      { value: '', label: allLabel },
      ...options.value,
    ])

  return { options, optionsWithAll }
}
```

理由（乔布斯式简洁 + OPC 自动化）：

- 一张规范 map，由 `GATEWAY_PLATFORMS` 排序（TypeScript 已经把它绑到
`AccountPlatform` 联合类型上 —— 将来加第 6 个平台只需要改一个文件）。
- **平台品牌名不做 i18n key**：Anthropic / OpenAI / Gemini / Antigravity / New API
在本仓库今天都没有翻译；现在引入分语种品牌字符串属于过早抽象。
- **创建账号表单的字段标签需要 i18n key**：channel_type / base_url / api_key 等
字段说明文字必须本地化，统一放在 `admin.accounts.newApiPlatform.`*
命名空间下（en/zh 两份 locale 同步加），见 §3.5。这与"品牌名不本地化"
并不冲突 —— 前者是品牌，后者是 UI 文案。
- 筛选变体写成函数（不是 computed），这样调用方能自己传本地化 "全部" 标签，
不必把它全局化。

### 3.2 创建账号 tab —— `CreateAccountModal.vue`

在现有 Antigravity 按钮之后（约第 139 行）追加第 5 个 segmented-control 按钮，
绑定 `form.platform = 'newapi'`。在现有 `antigravity` 块（约第 707 行）之后
紧接着加一个 `<div v-if="form.platform === 'newapi'">` 块，承载
`<AccountNewApiPlatformFields v-model:channelType="..." v-model:baseUrl="..." v-model:apiKey="..." :channel-type-options="..." :channel-types-loading="..." :channel-types-error="..." :selected-channel-type-base-url="..." />`。

数据接线：

- modal 打开时（或第一次 `form.platform === 'newapi'` 时）调用
`@/api/admin/channels` 的 `listChannelTypes()`，投影成 `{value, label}` pair。
- `selectedChannelTypeBaseUrl` 由所选 `channel_type` 行派生，**双重用途**：
① 作为输入框 placeholder，让运维即使不填也能看到上游官方 URL；
② 提交时若用户留空 `base_url`，自动回退到 channel-type 的默认 URL
（CreateAccountModal.vue:4104：`baseUrl = newapiBaseUrl.value.trim() || newapiSelectedBaseUrl.value`）。
这两个用途必须保持一致，未来重构者不得把它退化为"纯 placeholder"。
- 提交 → 调用现有 `createAccount()` 时带上 `platform: 'newapi'`、
顶层 `channel_type`（数字）、`base_url`、`api_key`、`name`。后端校验
`channel_type > 0`（admin_service.go:1565），所以前端无需重复这一守卫，
仅给一个 "必填" 的提示即可。
- **OAuth 流程必须 bypass**：`isOAuthFlow` 在 `form.platform === 'newapi'`
时强制为 false（newapi 仅 apikey），否则 template 的 step indicator
`v-if="isOAuthFlow"` 会对单步流程错误地渲染 "step 1/2"。

遵循 CLAUDE.md §5.x：`CreateAccountModal.vue` 来自上游；我们只**追加**一个
tab 和一个 `v-if` 块（不重写）。

### 3.3 展示正确性 —— `PlatformTypeBadge.vue`

今天第 74-79 行：

```ts
const platformLabel = computed(() => {
  if (props.platform === 'anthropic') return 'Anthropic'
  if (props.platform === 'openai') return 'OpenAI'
  if (props.platform === 'antigravity') return 'Antigravity'
  return 'Gemini'  // ← BUG：任何未知平台都被渲染成 Gemini
})
```

`platformClass` 和 `typeClass` 里有同样的反模式（默认回退 = 蓝色/Gemini 样式）。

修法：用显式的 `Record<AccountPlatform, …>` map（或等价的穷举 switch）替换隐式
default，**真未知平台走中性灰**而非"Gemini 蓝"。配色与 `gatewayPlatforms.ts:30`
的 cyan 一致。

```ts
const PLATFORM_LABEL: Record<AccountPlatform, string> = {
  anthropic:'Anthropic', openai:'OpenAI', gemini:'Gemini',
  antigravity:'Antigravity', newapi:'New API',
}
const PLATFORM_BG: Record<AccountPlatform, string> = {
  anthropic:'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400',
  openai:   'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  gemini:   'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',
  antigravity:'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',
  newapi:   'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400',
}
const PLATFORM_TYPE_BG: Record<AccountPlatform, string> = { /* 同形 */ }
```

> 实现备注：原型 commit 落在了等价的穷举 `switch` 上（已含中性灰 default 分支）。
> 行为与 Map 版一致，但"加第 6 个平台只改一处"的承诺要求 3 个 switch 同步改 3 处。
> 推荐 stage-3 把 3 个 switch 折叠成 import `gatewayPlatforms.ts` 已有的
> `SOFT_BADGE` map（line 25-31，已经包含全部 5 个平台的同款配色），
> 与"单一事实源"主张闭环。

### 3.4 GroupsView 选项替换

把 `GroupsView.vue:2813-2818` 和 `:2820-2826` 的字面量替换成 composable 调用。
筛选变体使用 `optionsWithAll(t('admin.groups.allPlatforms'))`。

### 3.5 涉及文件（原型）


| 路径                                                                            | 变更                                                                                  |
| ----------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `frontend/src/composables/usePlatformOptions.ts`                              | NEW —— composable                                                                   |
| `frontend/src/composables/__tests__/usePlatformOptions.spec.ts`               | NEW —— vitest 回归测试（AC-001 / AC-002）                                                 |
| `frontend/src/views/admin/GroupsView.vue`                                     | 把 2 处硬编码选项列表换成 composable（≤10 行 diff）                                               |
| `frontend/src/components/account/CreateAccountModal.vue`                      | + 第 5 个 segment 按钮 + `v-if newapi` 块 + `listChannelTypes` 接线 + `isOAuthFlow` bypass |
| `frontend/src/components/common/PlatformTypeBadge.vue`                        | 把隐式 default 替换为穷举分支；新增 `newapi`（青色），未知平台走中性灰                                        |
| `frontend/src/components/common/__tests__/PlatformTypeBadge.spec.ts`          | NEW —— vitest 回归测试（AC-003 / AC-004）                                                 |
| `frontend/src/types/index.ts`                                                 | `CreateAccountRequest` 增加可选顶层 `channel_type?: number`（让 newapi 提交路径在 TS 层面合法）       |
| `frontend/src/i18n/locales/en.ts`                                             | + `admin.accounts.newApiPlatform.*` 字段标签（channelType / baseUrl / apiKey 等）          |
| `frontend/src/i18n/locales/zh.ts`                                             | 同上 zh 版                                                                             |
| `.testing/user-stories/stories/US-018-admin-ui-newapi-platform-end-to-end.md` | NEW —— story                                                                        |
| `.testing/user-stories/index.md`                                              | + US-018 行                                                                          |
| `docs/approved/admin-ui-newapi-platform-end-to-end.md`                        | 本文档                                                                                 |


§1.5 review 后追加的文件（同一 PR）：


| 路径                                                           | 变更                                                                                        |
| ------------------------------------------------------------ | ----------------------------------------------------------------------------------------- |
| `frontend/src/utils/channelFormConversion.ts`                | NEW —— 抽出 ChannelsView 的 apiToForm/formToAPI 为纯函数，平台顺序参数化                                 |
| `frontend/src/utils/__tests__/channelFormConversion.spec.ts` | NEW —— 9 个 round-trip vitest case（含数据丢失 bug 的 NEGATIVE 反证）                                |
| `frontend/src/views/admin/ChannelsView.vue`                  | `platformOrder` 改为 `GATEWAY_PLATFORMS`；apiToForm/formToAPI 改为调用纯函数                        |
| `frontend/src/utils/platformColors.ts`                       | `Platform` 联合类型 + 9 张 variant map + `isPlatform()` + `platformLabel()` 全部加 `newapi`（cyan） |
| `frontend/src/components/admin/channel/types.ts`             | `getPlatformTagClass()` + `case 'newapi'`（cyan）                                           |
| `frontend/src/i18n/locales/{en,zh}.ts`                       | + `admin.groups.platforms.newapi` + `admin.accounts.platforms.newapi`                     |


不动 backend / Ent / Wire。不引入新依赖。

## 4. 风险分析


| 风险                                                                  | 概率        | 影响        | 缓解                                                                                                     |
| ------------------------------------------------------------------- | --------- | --------- | ------------------------------------------------------------------------------------------------------ |
| 已有 4 平台 UX 在 composable 替换后回归                                       | 中         | 中         | vitest 断言顺序 = GATEWAY_PLATFORMS；原型演示时手工点完 CreateAccountModal 中已有的 4 个 tab                              |
| 运维用错 channel_type / base_url 创建 newapi 账号导致调用失败                     | 高（UX，非回归） | 低（后端错误清晰） | `AccountNewApiPlatformFields` 已经接了 `fetchUpstreamModels` 用于自测；必填红星可见；`base_url` 留空时回退 channel-type 默认值 |
| 翻译缺口 —— "New API" 未本地化                                              | 低         | 低         | 品牌名今天都不本地化（已有 4 个平台都硬编码英文）—— 等项目 i18n 统一 pass 时一起做                                                     |
| `usePlatformOptions()` 被误用在不该出现 newapi 的 scope（如 SubscriptionsView） | 低         | 中         | Out-of-scope 列表明确排除这些 view；reviewer 检查调用点；建议 §5 加防漂移 preflight                                         |
| 后端在某个我们没审计到的地方拒绝 `Platform: "newapi"`                               | 低         | 高         | `admin_service.go:1565` 是 CreateAccount 唯一的平台检查；`CreateGroup` 接受任意字符串；US-008..014 的后端测试已经覆盖            |


## 5. 验收（stage-2 审批用的端到端 demo）

原型在 reviewer 拿到一份新的 dev 栈时能跑通以下流程，即视为可审批：

1. 打开 `/admin/groups`，点 「创建分组」，下拉里看到 **5** 个平台，包括 "New API"。成功创建一个 `newapi` 测试分组。
2. 打开 `/admin/accounts`，点 「创建账号」，看到 **5** 个平台 tab。点 "New API"。channel type 列表加载完成。选择 e.g. DeepSeek，填 base_url + api_key，保存。账号创建，列表刷新。
3. 新账号在列表中渲染为 "New API" + 青色徽章，**而非** "Gemini" + 蓝色。
4. 已有 4 个平台行为完全一致（无视觉/行为 diff）—— 重点点开 4 个老平台 tab，确认 step indicator (`isOAuthFlow`) 的显隐与升级前完全一致；newapi tab 选中时 step indicator **不**显示。
5. `pnpm lint:check && pnpm typecheck && pnpm test:unit` 全绿。
6. `./scripts/preflight.sh` 全绿（含 `dev-rules/scripts/check_approved_docs.py` 对本文档 frontmatter 的 R1-R5 校验）。

### 5.7 防漂移 preflight（建议在 stage-3 引入）

本设计修复的是"组织漂移"——`GATEWAY_PLATFORMS` 早就存在，但没有任何调用点用它生成选项。
为了避免下一个 PR 又往 admin view 里塞硬编码列表，建议在 `scripts/preflight.sh` 增加一段：

```bash
# 任何 admin view 出现字面量 ['anthropic','openai','gemini','antigravity'] 必须 fail
! rg -nP "\\['anthropic',\\s*'openai',\\s*'gemini',\\s*'antigravity'" \
    frontend/src --glob '!*/constants/gatewayPlatforms.ts' \
  || { echo "FAIL: hardcoded 4-platform list — use usePlatformOptions()"; exit 1; }
```

按 product-dev.mdc「能写成脚本/CI 检查/Rule 的，绝不依赖 Agent 每次自觉遵守」，
这是把"靠记忆"升级为"靠规则"。原型 PR 不强制要求，stage-3 第一个 PR 必须落地。

## 6. Stage-3 跟进（本次审批合并后）

> **2026-04-20 更新**：根据用户指令"全部关于 NewAPI 的都在 PR #19 内修复，
> 包括 stage-3 立项的全部"，原 stage-3 列表中 1-6 项已在 §1.6 全量并入 PR #19。
> 本节仅保留**真正不能在本 PR 落地**的延后项。

剩余 stage-3 项：

1. **§5.7 防漂移 preflight 段落落地** —— 必须在 §1-§5 全部切完且稳定运行
  一段时间后再加，否则会 fail 自己（同一 PR 同时落地不变量与最后一个违规
   消除，会让"是不是原型还有遗漏"无法被独立 review）。建议下一个针对
   `frontend/src/` 的 PR 一并落地。
2. `**PlatformTypeBadge.vue` 3 个 `switch` 折叠成 `SOFT_BADGE` map import**
  （§3.3 实现备注）—— 是 OPC 自动化升级（"加第 6 个平台只改一处"），但
   不影响 newapi 当前能力，可以延后。
3. `**BulkEditAccountModal.vue` 批量改 `channel_type` 守卫** —— 当前批量编辑
  通过 `useModelWhitelist`（§1.6 #13）已自动覆盖 newapi 模型映射；但批量
   改 `channel_type` 是破坏性操作（会一次性把多个 newapi 账号切换上游），
   要单独走 UX review，不在 PR #19 范围。

> 历史：原列表中的 `utils/platformColors.ts`（cyan 完整化）与
> `ChannelsView.vue:721` 审计已在 PR #19 review 期间被发现是**数据丢失 bug**
> 而非纯视觉补全，因此前置到 §1.5 一同合并；其余 stage-3 项已并入 §1.6，
> 不再是延后项。

## 7. 待审批的开放问题

> 已经被原型实现做出选择的项不再列在这里（避免审批者以为还要表态）。

1. **展示标签**："New API" / "NewAPI" / "newapi"？原型用 **"New API"**（与 channel-type 目录习惯一致）。
2. **配色**：cyan（`gatewayPlatforms.ts` 已声明）。批准还是覆盖？
3. **本地化策略**：品牌名继续保留英文（当前惯例）+ 字段标签走 i18n key。批准还是要求把品牌名也加 i18n？

