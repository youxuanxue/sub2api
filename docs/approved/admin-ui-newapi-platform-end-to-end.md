---
title: Admin UI — 第五平台 `newapi` 端到端可见性与可操作性
status: pending
approved_by: pending
approved_at: pending
authors: [agent]
created: 2026-04-20
related_prs: []
related_commits: []
related_stories: [US-017]
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
（ops-dashboard 筛选、错误透传规则、批量编辑、渐变色/折扣/按钮颜色变体）会列入
stage-3 跟进，但明确排除在本次原型之外。

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

- `AccountTableFilters.vue` / `OpsDashboardHeader.vue` / `ErrorPassthroughRulesModal.vue`
  的平台 picker —— 同样换成本 composable，但每个都有自己的筛选语义，需要单独 review。
- `EditAccountModal.vue` / `BulkEditAccountModal.vue` —— 不能让它们回归，但
  完整支持批量编辑 newapi 渠道有 UX 影响（批量改 channel_type 是破坏性操作），
  放到独立 review 里。
- `ChannelsView.vue:721` `platformOrder: GroupPlatform[]` 也是 4-元素硬编码
  数组，被 v-for 渲染分组 + 成员判定使用。需要先搞清楚"channels view 只渲染
  4 个 OAuth/订阅平台"是有意为之（newapi 用的是 channel_type 而非 channel
  名词，可能不属于本视图）还是同款组织漂移——属于"先审计再决定要不要切
  composable"，stage-3 单独一个 PR。
- `utils/platformColors.ts` —— 扩展 `Platform` 联合类型并把 `newapi` 加进全部
  9 张 variant map，让非 badge 表面也具备视觉完整性。
- `PlatformIcon.vue` —— 给 newapi 选品牌图标是设计决策不是 bug；目前的通用
  地球图标回退是可以接受的。
- `SubscriptionsView.vue` —— newapi 没有 OAuth 订阅这个概念，加上反而误导。

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
  字段说明文字必须本地化，统一放在 `admin.accounts.newApiPlatform.*`
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

| 路径 | 变更 |
| --- | --- |
| `frontend/src/composables/usePlatformOptions.ts` | NEW —— composable |
| `frontend/src/composables/__tests__/usePlatformOptions.spec.ts` | NEW —— vitest 回归测试（AC-001 / AC-002） |
| `frontend/src/views/admin/GroupsView.vue` | 把 2 处硬编码选项列表换成 composable（≤10 行 diff） |
| `frontend/src/components/account/CreateAccountModal.vue` | + 第 5 个 segment 按钮 + `v-if newapi` 块 + `listChannelTypes` 接线 + `isOAuthFlow` bypass |
| `frontend/src/components/common/PlatformTypeBadge.vue` | 把隐式 default 替换为穷举分支；新增 `newapi`（青色），未知平台走中性灰 |
| `frontend/src/components/common/__tests__/PlatformTypeBadge.spec.ts` | NEW —— vitest 回归测试（AC-003 / AC-004） |
| `frontend/src/types/index.ts` | `CreateAccountRequest` 增加可选顶层 `channel_type?: number`（让 newapi 提交路径在 TS 层面合法） |
| `frontend/src/i18n/locales/en.ts` | + `admin.accounts.newApiPlatform.*` 字段标签（channelType / baseUrl / apiKey 等） |
| `frontend/src/i18n/locales/zh.ts` | 同上 zh 版 |
| `.testing/user-stories/stories/US-017-admin-ui-newapi-platform-end-to-end.md` | NEW —— story |
| `.testing/user-stories/index.md` | + US-017 行 |
| `docs/approved/admin-ui-newapi-platform-end-to-end.md` | 本文档 |

不动 backend / Ent / Wire。不引入新依赖。

## 4. 风险分析

| 风险 | 概率 | 影响 | 缓解 |
| --- | --- | --- | --- |
| 已有 4 平台 UX 在 composable 替换后回归 | 中 | 中 | vitest 断言顺序 = GATEWAY_PLATFORMS；原型演示时手工点完 CreateAccountModal 中已有的 4 个 tab |
| 运维用错 channel_type / base_url 创建 newapi 账号导致调用失败 | 高（UX，非回归） | 低（后端错误清晰） | `AccountNewApiPlatformFields` 已经接了 `fetchUpstreamModels` 用于自测；必填红星可见；`base_url` 留空时回退 channel-type 默认值 |
| 翻译缺口 —— "New API" 未本地化 | 低 | 低 | 品牌名今天都不本地化（已有 4 个平台都硬编码英文）—— 等项目 i18n 统一 pass 时一起做 |
| `usePlatformOptions()` 被误用在不该出现 newapi 的 scope（如 SubscriptionsView） | 低 | 中 | Out-of-scope 列表明确排除这些 view；reviewer 检查调用点；建议 §5 加防漂移 preflight |
| 后端在某个我们没审计到的地方拒绝 `Platform: "newapi"` | 低 | 高 | `admin_service.go:1565` 是 CreateAccount 唯一的平台检查；`CreateGroup` 接受任意字符串；US-008..014 的后端测试已经覆盖 |

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

跟进项以 §1 Out-of-scope 列出的条目为单一事实源，每一项一个独立 PR。建议顺序：

1. `AccountTableFilters.vue` 切到 `usePlatformOptions()` —— 运维可见性最高
2. `OpsDashboardHeader.vue` 同上
3. `EditAccountModal.vue` 增加 newapi 编辑分支（不含批量）
4. `BulkEditAccountModal.vue` 加批量编辑守卫（`channel_type` 不允许批量改）
5. `utils/platformColors.ts` 把 `newapi` 加进 9 张 variant map（视觉完整性）
6. `ErrorPassthroughRulesModal.vue` 同 §1（运营紧迫性最低）
7. `PlatformTypeBadge.vue` 3 个 `switch` → import `gatewayPlatforms.ts` 的 `SOFT_BADGE` map（§3.3 实现备注）
8. `ChannelsView.vue:721` `platformOrder` 审计：先确认"channels view 只渲染 4 个
   OAuth/订阅平台"是有意为之还是组织漂移；如果是后者切到 composable，如果是
   前者在常量旁补一行注释说明排除原因（防止下一个 reviewer 重复怀疑）。
9. §5.7 的防漂移 preflight 段落落地（应在切完 §1-§3 之后，否则会 fail 自己）

## 7. 待审批的开放问题

> 已经被原型实现做出选择的项不再列在这里（避免审批者以为还要表态）。

1. **展示标签**："New API" / "NewAPI" / "newapi"？原型用 **"New API"**（与 channel-type 目录习惯一致）。
2. **配色**：cyan（`gatewayPlatforms.ts` 已声明）。批准还是覆盖？
3. **本地化策略**：品牌名继续保留英文（当前惯例）+ 字段标签走 i18n key。批准还是要求把品牌名也加 i18n？

