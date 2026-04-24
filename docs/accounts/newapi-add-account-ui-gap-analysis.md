# NewAPI（第五平台）添加账号 UI 缺口诊断与方案

- 状态: shipped
- 作者: agent
- 创建日期: 2026-04-23
- 合并日期: 2026-04-23
- 触发事件: 用户在 AWS prod 1.5.0 (`https://api.tokenkey.dev/admin/accounts`) 发现 添加账号 → NewAPI 路径下表单不完整、与上游 new-api 添加渠道 UX 显著不一致
- 关联文档:
  - `docs/approved/newapi-as-fifth-platform.md`（v1.4.0, shipped）
  - `docs/approved/admin-ui-newapi-platform-end-to-end.md`（v1.4.x, shipped, PR #19）
  - `docs/approved/newapi-followup-bugs-and-forwarding-fields.md`（v1.5.0, shipped, PR #29）
- 本次修复（D1-D4 实现）: PR #47（squash commit `942154d3`，含 27 个新 vitest 用例）
- 关联提交: `942154d3` （main 上的 squash commit；展开后对应分支上 7 个原子 commit：d72befa4 → d70f95fe → fc3d35fe → 5f00c529 → e76b9988 → d469705f → 95e566c9）

---

## 0. TL;DR

用户的两条观察都成立，但**根因不同**，不能合并成"一个 bug"：

| 路径 | 用户感知 | 真实根因（1.5.0 main HEAD） |
| ---- | -------- | ---------------------------- |
| 路径 1：开 modal → 直接点 NewAPI | 看不到 类型 / Base URL / API Key / 模型 / 获取模型列表 | (1) AccountNewApiPlatformFields **会**渲染 6 个字段（类型/baseUrl/apiKey/modelMapping/statusCodeMapping/openaiOrganization），但**全部在 BaseDialog 滚动区域内**，且 modal 高度容易把这部分顶到首屏可视区域之外。(2) 但**模型白名单/映射区**确实**不渲染**——因为 `accountCategory='oauth-based'`（resetForm 默认）+ 平台切到 `newapi` 时，没有任何 watcher 把 `accountCategory` 翻成 `'apikey'`，导致 `form.type` 卡在 `'oauth'`，而模型白名单/映射区的 v-if 是 `form.type === 'apikey' && form.platform !== 'antigravity'`。(3) "获取模型列表" 按钮**根本没有被任何模板使用**——composable `useTkAccountNewApiPlatform` 已经把 `fetchUpstreamModels` + `upstreamFetchConfig` 写好了，但 Create/Edit 两个 Modal 都还在用旧的 `listChannelTypes()` 直连方式，这个 composable 是**死代码**。 |
| 路径 2：先点 OpenAI → 选 API Key → 切换到 NewAPI | 有 类型 / Base URL / API Key / 模型，但没有 获取模型列表 | (1) 因为先选了 API Key，`accountCategory` 已经 = `'apikey'`，所以**通用 apikey 块**（line 890）的 v-if 通过 → 渲染了模型白名单/映射；同时 NewAPI 块（line 729）也渲染了 → **此时画面里同时存在 2 套 base_url 和 2 套 api_key 输入框**——上面那套 placeholder 还会写 `https://api.anthropic.com`（因为 placeholder 三元链没列 `newapi` 分支），构成**双源信息冲突**，而不是"路径 2 是好的"。(2) "获取模型列表" 缺失同路径 1。 |

简言之，**v1.5.0 部署在 prod 的 NewAPI 表单与原生 new-api 添加渠道 UX 之间存在 4 项实质性差距**：

1. **D1**：`accountCategory` 不随 `form.platform = 'newapi'` 自动切到 `apikey` → 路径 1 看不到模型选择区。
2. **D2**：通用 apikey 块在 newapi 平台下未短路 → 路径 2 出现重复字段（双 baseUrl + 双 apiKey），placeholder 还误导写 anthropic。
3. **D3**：`useTkAccountNewApiPlatform` composable（含 `获取模型列表` 能力）从未被 wire 进 Create/Edit Modal → 整个体系缺少「按 channel_type 拉取上游真实模型列表」的能力（按钮 + handler + i18n key 已经全部就绪）。
4. **D4**：`AccountNewApiPlatformFields.vue` 没有 models multiselect → 即使触发 `fetchUpstreamModels` 拉到列表，目前也无处显示和选择。模型白名单/映射依赖通用 apikey 块来兜，是 D1/D2 的**症状**，不是设计意图。

后面 §1-§7 给出每个差距的代码定位、上游 new-api 对照、修复方案、优先级与回归测试清单。**所有 4 项都属于「常规风险」级别**——动 admin UI 表单结构、有 Vue watcher / v-if 改动，但不触碰公共 API 契约、状态机、安全边界，可在单一 PR 内闭合。

---

## 1. 现状代码审计

### 1.1 表单结构（CreateAccountModal.vue, 1.5.0）

| 行号 | 区块 | v-if 条件 | 与 newapi 的关系 |
| ----: | -------- | --------- | ----------------- |
| 70-168 | Platform segmented control | 无 | newapi 是第 5 个 button（cyan 高亮，US-017 prototype） |
| 172-265 | AccountType row（Anthropic 的 OAuth/API Key/Bedrock 三选一） | `form.platform === 'anthropic'` | newapi 不展示 |
| 269-324 | AccountType row（OpenAI 的 OAuth/API Key 二选一） | `form.platform === 'openai'` | newapi 不展示 |
| 327-664 | AccountType row（Gemini 的 OAuth/API Key + Tier 选择） | `form.platform === 'gemini'` | newapi 不展示 |
| 667-722 | AccountType row（Antigravity 的 OAuth/Upstream 二选一） | `form.platform === 'antigravity'` | newapi 不展示 |
| **729-743** | **NewAPI 字段块（AccountNewApiPlatformFields）** | **`form.platform === 'newapi'`** | **核心；包含 6 个字段，无 models 选择** |
| 745-769 | Antigravity Upstream 配置 | `form.platform === 'antigravity' && antigravityAccountType === 'upstream'` | newapi 不展示 |
| 773-860 | Antigravity 模型限制 | `form.platform === 'antigravity'` | newapi 不展示 |
| 862-887 | Add Method（Anthropic OAuth 的 oauth/setup-token） | `form.platform === 'anthropic' && isOAuthFlow` | newapi 不展示 |
| **890-1170** | **通用 apikey 块（base url、api key、Model Restriction、Pool Mode、Custom Error Codes）** | **`form.type === 'apikey' && form.platform !== 'antigravity'`** | **关键陷阱：当 `form.type==='apikey'` 时，newapi 也命中此块——见 §2.2** |
| 1759-1905 | Temp Unsched Rules | 无 | 所有平台共用 |
| 2319-2358 | 代理 / 并发 / 优先级 / 计费倍率 / 过期 / 自动暂停 / 分组 | 无 | 所有平台共用 |

### 1.2 状态变量与 watcher（关键 4 处）

```ts
// L3089
const accountCategory = ref<'oauth-based' | 'apikey' | 'bedrock'>('oauth-based')
// L3090
const addMethod = ref<AddMethod>('oauth')

// L3347
const form = reactive({
  ...
  platform: 'anthropic' as AccountPlatform,
  type: 'oauth' as AccountType,   // ← 默认 'oauth'
  ...
})

// L3443-3464  watcher A：把 (accountCategory, addMethod, agType) 映射为 form.type
watch(
  [accountCategory, addMethod, antigravityAccountType],
  ([category, method, agType]) => {
    if (form.platform === 'antigravity' && agType === 'upstream') {
      form.type = 'apikey'; return
    }
    if (form.platform === 'anthropic' && category === 'bedrock') {
      form.type = 'bedrock' as AccountType; return
    }
    if (category === 'oauth-based') {
      form.type = method as AccountType  // 'oauth' | 'setup-token'
    } else {
      form.type = 'apikey'
    }
  },
  { immediate: true }
)

// L3467-3524  watcher B：当 form.platform 改变时重置 platform-specific 字段
watch(
  () => form.platform,
  (newPlatform) => {
    apiKeyBaseUrl.value = newPlatform === 'openai' ? '...' :
                         newPlatform === 'gemini' ? '...' : 'https://api.anthropic.com'
    allowedModels.value = []
    modelMappings.value = []
    ...
    // ← 没有任何"如果 newPlatform === 'newapi' 则 accountCategory.value = 'apikey'"的逻辑
    ...
  }
)
```

**Watcher A 的失效条件**：当用户只改 `form.platform` 而**不改** `accountCategory / addMethod / antigravityAccountType` 时，watcher A 不会被触发，`form.type` 保持原值。这正是路径 1 的成因（`accountCategory='oauth-based'` → `form.type='oauth'` → apikey 块隐藏 → 没有 Model Restriction 区）。

**Watcher B 缺一段 newapi 分支**：参照 antigravity 在 L3486-3488 自动把 `accountCategory.value = 'oauth-based'`、`antigravityAccountType.value = 'oauth'`，newapi 也应该有「平台切换为 newapi 时自动把 accountCategory 翻成 apikey」的对称分支（newapi 是 apikey-only，没有 OAuth 流）。

### 1.3 提交时的"补救"逻辑（不是替代修复）

```ts
// L4120-4186
if (form.platform === 'newapi') {
  ...
  await doCreateAccount({
    ...
    type: 'apikey',           // ← 硬编码，覆盖 form.type
    channel_type: newapiChannelType.value,
    credentials,
    ...
  })
  return
}
```

**含义**：提交链路是对的，后端永远收到 `type='apikey'`，所以**写库结果**与原生 new-api channel 等价。但**渲染期间**仍然以 `form.type` 作为 v-if 依据，于是出现"渲染状态 ≠ 提交状态"的内部不一致——D1 / D2 的根本来源。

### 1.4 useTkAccountNewApiPlatform 是死代码

`frontend/src/composables/useTkAccountNewApiPlatform.ts` 是 commit `6d562043`（v1.4.0 之前的 tokenkey 基线）就引入的完整 composable，包含：

- `channelTypes` / `channelTypeModels` 双 cache（共享 inflight，保证多 modal 复用同一份 catalog）
- `channelTypeOptions`（带 channel_type 编号 label）
- `selectedChannelTypeBaseUrl`（按选中类型自动 prefill base_url）
- `fillPresets`（按 channel_type 推导预设模型列表）
- **`fetchUpstreamModels()`**——已经接好 `POST /admin/channel-types/fetch-upstream-models`，并写入 `lastUpstreamModels` + `allowedModels`
- **`upstreamFetchConfig`**——`{ show, loading, disabled, onFetch }` 即插即用结构
- 对 `form.channel_type` 的 watcher（自动同步 base_url + 模型白名单）

**Grep 验证**：

```
$ rg -l 'useTkAccountNewApiPlatform' frontend/src
frontend/src/composables/useTkAccountNewApiPlatform.ts        # 仅自身
```

PR #19（`3a43a109`）当时为了"prototype 范围最小化"绕过了这个 composable，直接在 Create/Edit Modal 各写了一份 `listChannelTypes()` 调用。这是**已经签了字的临时方案**（见 docs/approved/admin-ui-newapi-platform-end-to-end.md §6 stage-3 backlog），但 stage-3 follow-up 至今没合。结果：

1. PR #29（v1.5.0）补了 3 个转发字段，但仍然只 wire 了 channel_type 列表，没接 `fetchUpstreamModels`。
2. composable 的全部能力——**包括获取模型列表按钮所需的 onFetch 与 disabled 计算**——都未被使用。

### 1.5 与原生 new-api 添加渠道的对照

`/new-api/web/src/components/table/channels/modals/EditChannelModal.jsx`（共 3932 行，删减为表单核心字段）：

| 上游字段 | TK 现状 | 差距 |
| -------- | ------- | ---- |
| 类型 (`type`, Channel Type) | NewAPI 字段块第 1 项 | ✅ 一致 |
| 名称 (`name`) | 顶部 `accountName` | ✅ 一致（语义对齐：上游"渠道名称"= TK"账号名称"） |
| 密钥 (`key`) | NewAPI 字段块第 3 项 | ✅ 一致（密钥占位符上游会按 `type` 分文案，TK 用通用 placeholder） |
| 代理 / 标签 / 优先级 / 权重 | 通用区，所有平台共用 | ✅ 一致 |
| **API 地址 (`base_url`)** | NewAPI 字段块第 2 项 | ✅ 一致 |
| **模型 (`models`, multi-select)** | **❌ NewAPI 字段块没有；通用 apikey 块的"模型白名单"是替代品** | **D1 / D2 / D4** |
| **「填入相关模型」按钮** | 通用 apikey 块有 ModelWhitelistSelector，但要先把 form.type 翻成 apikey 才看得见 | 等价能力可达，但路径 1 完全看不见（D1） |
| **「获取模型列表」按钮** | **❌ 完全没渲染**；后端 + composable 已就绪 | **D3** |
| **「填入所有模型」/「清除所有模型」/ 模型分组** | 通用 apikey 块部分覆盖（whitelist/mapping 切换） | 不在 P0 范围 |
| 「测试模型」一键调用 | TK 有 AccountTestModal（独立入口） | ✅ 等价 |
| 「Ollama 模型管理」（type=4 专用） | 暂无 | 不在 P0 范围 |
| `model_mapping` | NewAPI 字段块第 4 项 | ✅ 一致（PR #29） |
| `status_code_mapping` | NewAPI 字段块第 5 项 | ✅ 一致（PR #29） |
| `openai_organization` | NewAPI 字段块第 6 项 | ✅ 一致（PR #29） |
| `param_override` / `force_format` | TK 暂无 UI；bridge 已支持透传部分参数 | 不在 P0 范围 |

**结论**：核心 6 字段 + 转发 3 字段已经齐全；**真正的体验差距在「模型」区**——既缺 fetch 按钮，也缺一个 newapi 平台下"自然"的模型选择入口（不需要绕道 apikey 类别）。

---

## 2. 故障路径解剖

### 2.1 路径 1：fresh open + 直接点 NewAPI

**用户操作**：

1. 进入 `/admin/accounts`
2. 点击右上角 "添加账号" → modal 打开（`watch(props.show)` 触发，调用 `listChannelTypes()` 启动加载，`accountCategory='oauth-based'`，`form.platform='anthropic'`，`form.type='oauth'`）
3. 点击 Platform segment 中的 "New API" → `form.platform='newapi'`

**Vue 响应链**：

| Effect | 触发 | 实际行为 |
| ------ | ---- | -------- |
| `form.platform === 'newapi'` 的 v-if 重新求值 | `form.platform` 变更 | **NewAPI 字段块被插入 DOM**（6 个字段 + 占位提示） |
| watcher B `() => form.platform` | `form.platform` 变更 | 重置 `apiKeyBaseUrl='https://api.anthropic.com'`、`allowedModels=[]`、`modelMappings=[]`、`oauth.resetState()` 等。**没有调整 `accountCategory`** |
| watcher A `[accountCategory, addMethod, antigravityAccountType]` | 依赖未变 | **不触发**。`form.type` 保持 `'oauth'` |
| `form.type === 'apikey' && form.platform !== 'antigravity'` 的 v-if | 求值结果 false | 通用 apikey 块（含 BaseURL、API Key、Model Restriction、Pool Mode、Custom Error Codes）**全部不渲染** |
| Anthropic / OpenAI / Gemini / Antigravity 的 AccountType row（v-if 各自的 platform） | newapi 不命中 | 不渲染 |

**最终 DOM**（路径 1）：

```
[账号名称]
[备注]
[平台 segmented control: New API 高亮]
[NewAPI 字段块: 渠道类型 / Base URL / API Key / model_mapping / status_code_mapping / openai_organization]   ← 这 6 项实际存在
[临时不可调度 toggle]
[代理]
[并发数 / 负载因子 / 优先级 / 计费倍率]
[过期时间 / 自动暂停]
[分组]
```

**用户感知差距**：

- ① 用户截图里"看不到"NewAPI 字段块——但**字段块确实在 DOM 里**，只是 modal `class="modal-body"` 是滚动容器（`max-h-[80vh]; overflow-y:auto`），高分辨率显示器上首屏顶到底就把"NewAPI 字段块"挤到了"平台"和"临时不可调度"之间——视觉上也许只看到一个折叠/截断状态。**修复需要做的不是补字段，而是减少视觉混淆**——见 §3.D1。
- ② "看不到模型 / 获取模型列表"——这个**不是错觉**，因为 Model Restriction 区只在 apikey 块里，而 apikey 块在路径 1 里 v-if=false 完全不渲染。**这才是路径 1 真正的渲染缺口**。

### 2.2 路径 2：先选 OpenAI/API Key → 再切到 NewAPI

**用户操作**：

1. 进入 `/admin/accounts` → "添加账号"
2. 点 OpenAI → `form.platform='openai'`，watcher B 触发，但 `accountCategory` 仍 = `'oauth-based'`
3. 点 API Key（OpenAI 的 AccountType）→ `accountCategory='apikey'`，watcher A 触发 → `form.type='apikey'`
4. 通用 apikey 块开始渲染（BaseURL=`https://api.openai.com`、API Key、Model Restriction openai 预设）
5. 点 New API → `form.platform='newapi'`，watcher B 触发：`apiKeyBaseUrl='https://api.anthropic.com'`（**即时把上面那套 baseUrl 写成 anthropic 默认！**）、`allowedModels=[]`、`modelMappings=[]`
6. watcher A 不触发（`accountCategory` 没改），`form.type` 仍 = `'apikey'`

**最终 DOM**（路径 2）：

```
[账号名称][备注][平台]
[NewAPI 字段块: 渠道类型 / Base URL / API Key / model_mapping / status_code_mapping / openai_organization]

[通用 apikey 块开始]
  [Base URL: 输入框, placeholder="https://api.anthropic.com"]   ← 重复 + placeholder 误导
  [API Key: 输入框, placeholder="sk-ant-..."]                    ← 重复 + placeholder 误导
  [Model Restriction]
    [whitelist/mapping 切换]
    [whitelist 模式: ModelWhitelistSelector(platform='newapi') → 可选模型]   ← 这就是用户看到的"模型"
  [Pool Mode]
  [Custom Error Codes]
[通用 apikey 块结束]

[Temp Unsched / 代理 / 并发 / ...]
```

**用户感知差距**：

- ✅ 看到了"模型"区（实际上是通用 apikey 块的 ModelWhitelistSelector）
- ❌ 没有"获取模型列表"按钮（D3，所有路径共有）
- ❌ 用户**没意识到**自己看到的是 2 套 baseUrl + 2 套 apiKey 同时存在；提交时只有 NewAPI 字段块那套被使用（line 4129-4140 直接读 `newapiBaseUrl.value` / `newapiApiKey.value`，不读 `apiKeyBaseUrl.value` / `apiKeyValue.value`）。**通用 apikey 块那套 baseUrl/apiKey 输入是"幻影字段"**——用户填了不会被提交，但 placeholder 写 anthropic 又会让用户填错。这是 D2 的具体爆点。

### 2.3 共有缺口：获取模型列表（D3）

无论路径 1 还是路径 2，"获取模型列表"按钮在 v1.5.0 deployed bundle 中都**不存在**：

```bash
$ curl -sL https://api.tokenkey.dev/assets/AccountsView-BfNKhgCU.js \
   | grep -oE 'fetchUpstreamModels|获取模型列表' | sort -u
（空）
```

验证：

```bash
$ rg -l 'fetchUpstreamModels|获取模型列表' frontend/src/api frontend/src/composables
frontend/src/api/admin/channels.ts                   # 接口已就绪
frontend/src/composables/useTkAccountNewApiPlatform.ts # composable 已就绪

$ rg -l 'fetchUpstreamModels|upstreamFetchConfig' frontend/src/components
（空 — Modal 没用）
```

**后端**：`POST /api/v1/admin/channel-types/fetch-upstream-models` 在 v1.4.0 已经 ship（`backend/internal/server/routes/admin_tk_channel_routes.go:16` + `backend/internal/integration/newapi/fetch_upstream_models.go`），prod 可调用。

**i18n**：`zh.ts` / `en.ts` 已经埋了 `fetchUpstreamModelsNeedUrlKey / fetchUpstreamModelsEmpty / fetchUpstreamModelsSuccess / fetchUpstreamModelsFailed` 4 个 key（与 composable 对齐）。**只差模板的 1 个 button + 1 段 onClick**。

### 2.4 字段共存的 Vue 状态真相表

| 路径 | `form.platform` | `accountCategory` | `form.type` | NewAPI 块渲染？ | 通用 apikey 块渲染？ | 模型区可见？ | 获取模型列表？ |
| ---- | --------------- | ----------------- | ----------- | --------------- | -------------------- | ------------ | -------------- |
| 1（fresh + NewAPI） | `'newapi'` | `'oauth-based'` | `'oauth'` | ✅ | ❌ | ❌ | ❌ |
| 2（OpenAI/Key → NewAPI） | `'newapi'` | `'apikey'` | `'apikey'` | ✅ | ✅（含 anthropic 误导 placeholder） | ✅（whitelist newapi 预设） | ❌ |
| 3（Anthropic/Key → NewAPI） | `'newapi'` | `'apikey'` | `'apikey'` | ✅ | ✅（同 2） | ✅ | ❌ |
| 4（fresh + Anthropic OAuth） | `'anthropic'` | `'oauth-based'` | `'oauth'` | ❌ | ❌ | ❌（OAuth 流没有模型限制） | n/a |
| 5（fresh + OpenAI Key） | `'openai'` | `'apikey'` | `'apikey'` | ❌ | ✅ | ✅（openai 预设） | n/a |

期望（修复后）：

| 路径 | `form.platform` | `accountCategory` | `form.type` | NewAPI 块 | 通用 apikey 块 | 模型区 | 获取模型列表 |
| ---- | --- | --- | --- | --- | --- | --- | --- |
| 任意切到 newapi | `'newapi'` | `'apikey'`（自动翻） | `'apikey'` | ✅（含 models multiselect + 获取按钮） | ❌（newapi 短路） | ✅（在 NewAPI 块内部） | ✅（按 channel_type fetchable 判断显隐） |

---

## 3. 修复方案（按优先级）

### D1 / D2 联合修复：watcher B 加 newapi 分支 + 通用 apikey 块对 newapi 短路

**最小动作**（CreateAccountModal.vue + EditAccountModal.vue 各 1 处）：

1. **watcher B（CreateAccountModal.vue:3467）追加 newapi 分支**：

   ```ts
   watch(() => form.platform, (newPlatform) => {
     ...
     if (newPlatform === 'newapi') {
       // newapi 是 apikey-only：把 accountCategory 翻到 apikey，让 form.type 链路与提交链路一致
       accountCategory.value = 'apikey'
     } else if (newPlatform === 'antigravity') {
       accountCategory.value = 'oauth-based'
       antigravityAccountType.value = 'oauth'
     }
     ...
   })
   ```

   **副作用**：watcher A 因 `accountCategory` 变化被触发 → `form.type='apikey'`；通用 apikey 块的 v-if 通过 → 之前的 D1 表面症状（"看不到模型"）解除。**但** 这立刻引发 D2（双重字段）。所以 D1 / D2 必须**同 PR 一起改**，不能分开。

2. **通用 apikey 块 v-if 增加 `&& form.platform !== 'newapi'` 短路**（CreateAccountModal.vue:890）：

   ```html
   <div v-if="form.type === 'apikey' && form.platform !== 'antigravity' && form.platform !== 'newapi'" class="space-y-4">
     ...
   </div>
   ```

   **副作用**：路径 2 不再出现重复 baseUrl/apiKey；同时**模型白名单/映射区也跟着隐藏**——所以必须把它转移到 NewAPI 字段块内部（D4 同时落地）。

3. **EditAccountModal.vue** 没有 D1（编辑时 `form.platform` 已经从 `account.platform` 锁死）但有 D2 的对称问题；同样把通用 apikey 块的 newapi 短路加上。

**风险**：

- 中：watcher 加分支可能破坏 antigravity-upstream / bedrock 的现有路径 → **必须**回归（见 §6）。
- 低：通用 apikey 块的 i18n key 与 NewAPI 块不完全对齐（模型预设按 `platform='newapi'` 已对齐，但"自定义错误码"、"Pool Mode"未在 newapi 路径出现过——本次本来就不该展示，因为 newapi 走 bridge 不走 OpenAI WS pool）。

### D3：补「获取模型列表」按钮（核心修复）

**最小动作**（AccountNewApiPlatformFields.vue 模板尾部 + CreateAccountModal.vue / EditAccountModal.vue wire props）：

1. **AccountNewApiPlatformFields.vue 增加 fetch button + models multiselect**：

   ```vue
   <script setup lang="ts">
   ...
   const allowedModels = defineModel<string[]>('allowedModels', { default: () => [] })
   const upstreamFetch = defineProps<{
     fetchEnabled: boolean
     fetchLoading: boolean
     fetchDisabled: boolean
     onFetch: () => Promise<void> | void
   }>()
   ...
   </script>

   <template>
     ...
     <!-- 模型 multiselect + 获取模型列表按钮 -->
     <div>
       <label class="input-label">{{ t('admin.accounts.newApiPlatform.models') }}</label>
       <ModelWhitelistSelector v-model="allowedModels" :platform="'newapi'" />
       <div class="mt-2 flex items-center gap-2">
         <button v-if="upstreamFetch.fetchEnabled"
                 type="button"
                 :disabled="upstreamFetch.fetchDisabled || upstreamFetch.fetchLoading"
                 @click="upstreamFetch.onFetch"
                 class="btn btn-secondary">
           <Icon v-if="upstreamFetch.fetchLoading" name="spinner" size="sm" class="animate-spin" />
           {{ t('admin.accounts.newApiPlatform.fetchUpstreamModels') }}
         </button>
         <span class="text-xs text-gray-500">
           {{ t('admin.accounts.newApiPlatform.fetchUpstreamModelsHint') }}
         </span>
       </div>
     </div>
     ...
   </template>
   ```

2. **CreateAccountModal.vue / EditAccountModal.vue 接入 composable**：

   把现有的 `listChannelTypes()` 直连方式 + 6 个 ref + 局部 channelTypeOptions/selectedBaseUrl 替换为：

   ```ts
   import { useTkAccountNewApiPlatform } from '@/composables/useTkAccountNewApiPlatform'
   import { isNewApiUpstreamFetchableChannelType } from '@/constants/newApiUpstreamFetchableChannelTypes'

   const newapiPlatform = useTkAccountNewApiPlatform({
     form: reactive({ get channel_type() { return newapiChannelType.value } }),
     isNewapi: () => form.platform === 'newapi',
     baseUrl: newapiBaseUrl,
     apiKey: newapiApiKey,
     allowedModels,
     lastUpstreamModels,
     fetchLoading,
   })
   ```

   把 `:channel-type-options="newapiChannelTypeOptions"` 等 4-5 个绑定改成 `:channel-type-options="newapiPlatform.channelTypeOptions"`。新增一组 fetchEnabled/fetchLoading/fetchDisabled/onFetch 4 个 prop 把 `upstreamFetchConfig` 散下去。

   **效果**：~80 行重复代码合并为 1 个 composable 调用（OPC 杠杆）；获取模型列表按钮自动生效；`MODEL_FETCHABLE_TYPES`（已对齐上游 18 个 channel_type）会自动控制按钮显隐。

3. **删掉**：CreateAccountModal.vue 与 EditAccountModal.vue 中各自重复的 `newapiChannelTypes/newapiChannelTypesLoading/newapiChannelTypesError/newapiChannelTypeOptions/newapiSelectedBaseUrl` ref/computed（共 ~30 行 × 2）和 `listChannelTypes()` import + onShow watcher 里的 fetch 调用（替换为 `newapiPlatform.bootstrapNewapiCatalog()`）。

**风险**：

- 中：composable 接入触动 reactivity 边界（newapiChannelType 变成 watcher 源），需要回归"切换 channel_type → 自动 prefill base_url + 自动填模型预设"两条已有路径。
- 低：i18n 加 2 个 key（`models / fetchUpstreamModels / fetchUpstreamModelsHint`），其中 fetch* 已存在，只需补 `models` 与 hint。

### D4：把模型 multiselect 收口到 NewAPI 字段块

D4 与 D3 已经在上面合并（在 AccountNewApiPlatformFields.vue 内部加 `<ModelWhitelistSelector v-model="allowedModels" :platform="'newapi'" />`），不需要单独处理。

**唯一需要单独决定的**：是否同时支持 newapi 平台的 **模型映射模式**（whitelist vs mapping toggle）？

- **方案 A（保守）**：只放 whitelist 选择器；映射模式对 newapi 不做（与上游 new-api 一致——上游也只有 models multiselect，没有 mapping toggle，model_mapping 是独立的 JSON 字段，已经是 NewAPI 块第 4 项）。
- **方案 B（功能对齐 TK 其他平台）**：复用通用 apikey 块的 whitelist/mapping toggle，等于在 NewAPI 字段块内部嵌一个 ModelRestriction 子组件。

**推荐方案 A**：保持与上游 new-api UX 一致；TK 已经有 `model_mapping` JSON 字段（NewAPI 块第 4 项），功能上 mapping 已经覆盖。Whitelist 是更直观的"选模型"入口。

### D0（潜在）：路径 1 视觉首屏剪裁

如果用户截图确实是"NewAPI 字段块没渲染"，那 D0 不存在；如果只是滚动可见性问题，D0 加一个"切到 newapi 时滚回顶部"也不必做——D1 完成后 modal 高度自然增加，用户看到的"看不见的字段"会从视觉上消失（因为模型选择就在 NewAPI 块内部，不再出现"上面字段空一截下面突然冒出 apikey 块"的视觉断裂）。所以 D0 不列入修复范围。

---

## 4. 推荐落地节奏

按"乔布斯精品意识 + OPC 杠杆"原则，**所有 4 项收口在一个 PR**，避免半成品体验：

```
PR：feat(admin-ui-newapi): align add-account UI with native new-api channel form (D1-D4)

Scope:
- D1: watcher B 加 newapi → accountCategory='apikey' 自动同步分支
- D2: 通用 apikey 块 v-if 增加 form.platform !== 'newapi' 短路（Create + Edit）
- D3: AccountNewApiPlatformFields 加 ModelWhitelistSelector + 获取模型列表 button
- D4: Create/Edit Modal 接入 useTkAccountNewApiPlatform composable，删掉重复的 listChannelTypes 直连分支
- i18n: 补 admin.accounts.newApiPlatform.models / modelsHint / fetchUpstreamModelsHint（zh + en）
- 测试：vitest 覆盖 §6 所列 5 条 AC

Out-of-scope（stage-2）:
- "填入相关模型"/"复制所有模型"/"清除所有模型" 下拉菜单（上游有，TK 暂不做）
- Ollama 模型管理子 modal（type=4 专用）
- 模型卡片显示 channel_type 已知模型分组
```

风险等级：**常规风险**（修改 admin UI 表单结构 + 1 处 watcher + 1 处 v-if + 引入 composable，不涉及 API 契约/状态机/安全边界）。按 product-dev.mdc"常规风险默认路径：单 PR + 实现 + 测试 + preflight"。

---

## 5. 与已有 approved 文档的关系

- `docs/approved/admin-ui-newapi-platform-end-to-end.md`：本设计 stage-3 backlog 第一句"批量编辑 / `EditAccountModal.vue`"已经在 PR #19 内合入，但该文档**没有显式列**「fetchUpstreamModels button + models multiselect」缺口——它假设了"原型先把 6 个核心字段补齐就够"，把 models 选择默默委托给了通用 apikey 块。本诊断把该委托关系**作为 bug** 提起：路径 1 委托链断裂（accountCategory 不联动），路径 2 委托链通了但产生 ghost 字段。
- `docs/approved/newapi-followup-bugs-and-forwarding-fields.md`：补齐了 model_mapping/status_code_mapping/openai_organization 3 个**transit/forwarding** 字段，与本次「modelS（多选模型）+ 获取模型列表」**正交**——前者是"转发时怎么改写"，后者是"哪些模型可以走"。两条线都补齐才算与原生 new-api channel 表单对齐。
- `docs/approved/newapi-as-fifth-platform.md` §6 deferred items：原文明确说 "frontend：`platformOptions` 是否含 newapi 由 admin UI 决定，不在本 design 范围"，本诊断属于这条 deferred 工作的 stage-3 收尾。

修复 PR 落地后，建议：

1. 把本文 promote 为 `docs/approved/`（status: shipped, related_prs 填实际 PR 号）。
2. 在 `docs/approved/admin-ui-newapi-platform-end-to-end.md` §6 stage-3 backlog 中把对应条目划掉，加上 "see docs/approved/<本文档名>"。
3. PR 自带 vitest 用例（见 §6），**不**走完整 Story 路径（不命中 product-dev.mdc 的高风险升级条件）。

---

## 6. 测试与回归（vitest）

按 test-philosophy.mdc 默认路径：核心正向 + 核心负向 + 回归保护。

**核心 AC（必须）**：

| AC | 路径 | 期望 |
| -- | ---- | ---- |
| AC-1 正向 | fresh open → 点 NewAPI | NewAPI 字段块（含 models multiselect + 获取模型列表 button）渲染；通用 apikey 块不渲染；`form.type === 'apikey'` |
| AC-2 正向 | NewAPI → 选 channel_type=14 (deepseek) | base_url 自动 prefill 为 `https://api.deepseek.com`；获取模型列表 button 启用（14 在 fetchable set） |
| AC-3 正向 | NewAPI → 选 channel_type=14 → 输入 api_key → 点获取模型列表 | 调用 `POST /admin/channel-types/fetch-upstream-models` 一次；成功响应后 ModelWhitelistSelector 选中拉到的模型 |
| AC-4 负向 | NewAPI → channel_type=14 → 不填 api_key → 点获取模型列表 | button disabled；不发请求；提示 `fetchUpstreamModelsNeedUrlKey` |
| AC-5 回归 | OpenAI/Key → 切到 NewAPI | 通用 apikey 块的 baseUrl/apiKey 输入框消失（不再出现"双 baseUrl"）；NewAPI 字段块的 baseUrl/apiKey 是唯一输入源 |

**回归保护（必须）**：

| 场景 | 期望 |
| ---- | ---- |
| Anthropic OAuth + setup-token | 不受 watcher 改动影响（accountCategory='oauth-based' 不变） |
| Antigravity Upstream | 不受影响（特殊路径，watcher B 已有 antigravity 分支） |
| Bedrock | 不受影响 |
| OpenAI/API Key（不切到 newapi） | 通用 apikey 块照常渲染 |
| EditAccountModal 编辑已有 newapi 账号 | 通用 apikey 块隐藏；NewAPI 字段块出现；models multiselect 用账号现有 allowed_models 初始化 |

**测试文件位置**：

- `frontend/src/components/account/__tests__/CreateAccountModal.newapi.spec.ts`（新建）
- `frontend/src/components/account/__tests__/EditAccountModal.newapi.spec.ts`（新建或扩展现有）

mock：`adminAPI.channels.listChannelTypes` / `listChannelTypeModels` / `fetchUpstreamModels` 用 `vi.spyOn`。

---

## 7. 与 prod 现状的对照（observability check）

从 `scripts/fetch-prod-logs.sh`（GREP_PATTERN=`channel.type|channel-types|admin/account` SINCE=2h）拉到的 8 条日志：

```
GET  /admin/accounts                               200
GET  /api/v1/admin/accounts                        200
POST /api/v1/admin/accounts/today-stats/batch      200
GET  /api/v1/admin/accounts/{id}/usage             200
GET  /api/v1/admin/channel-types                   200    ← 用户打开 modal 触发
```

**没有** `POST /admin/channel-types/fetch-upstream-models` 调用——与 §1.4 "composable 是死代码" 的代码侧结论一致：**用户即使想拉模型列表，UI 上也没入口**。

prod 行为侧没有 5xx / 拒绝 / 慢请求 → **本诊断不是稳定性事件**，是**功能性 UX 缺口**。

---

## 8. 修复落地记录（2026-04-23）

按照 §3-§7 的方案在 PR #47 同一分支内闭合了 D1-D4，**未拆 PR**（OPC + 单一用户意图）。

### 8.1 文件变更摘要

| 文件 | 变更 | 对应 D# |
| ---- | ---- | ------- |
| `frontend/src/composables/useTkAccountNewApiPlatform.ts` | **业务逻辑唯一收口**：所有 newapi modal 表单状态（9 个 ref）+ 衍生 props（5 个 computed）+ channel_type 切换自动 prefill base_url 的 watcher + `handleFetchUpstreamModels()` 副作用 + `bootstrap` / `reset` / `populateFromAccount` / `buildSubmitBundle` 4 个生命周期方法。Create / Edit Modal 共用一份逻辑，无重复 | 全部 |
| `frontend/src/composables/useNewApiChannelTypeModels.ts` | **删除**（实测无消费者；channel-type 静态预设模型方案已让位于「获取模型列表」拉真实列表的方案） | — |
| `frontend/src/components/account/AccountNewApiPlatformFields.vue` | 新增 `allowedModels / modelMappings / restrictionMode` 三个 defineModel + whitelist↔mapping toggle + ModelWhitelistSelector(`platform=newapi`) + 「获取模型列表」button（emit fetch-models）；移除 `modelMapping` JSON textarea（保留 defineModel 兼容老父组件 v-model） | D3 + D4 |
| `frontend/src/components/account/CreateAccountModal.vue` | **薄 wiring**：watcher B 加 `newapi` 分支自动 `accountCategory='apikey'`；通用 apikey 块 v-if 增加 `&& form.platform !== 'newapi'`；从 composable destructure 9 个 ref + 5 个 computed + 4 个方法；模板用 v-model 透传；`onShow` 调 `bootstrap()`；`resetForm` 调 `reset()`；newapi 提交分支调 `buildSubmitBundle('create')`，无内联校验/JSON 解析 | D1 + D2 + 收口 |
| `frontend/src/components/account/EditAccountModal.vue` | **薄 wiring**：通用 apikey 模型限制块 v-if 增加 `&& account.platform !== 'newapi'`；从 composable destructure 同样的 ref/computed/方法（多传 `storedAccount` 让 fetch 走 stored credential 路径）；`syncFormFromAccount` 调 `populateFromAccount({channel_type, credentials})`，模式推断由 composable 负责；submit 调 `buildSubmitBundle('edit')`，仅在 wrapper 处叠加 currentCredentials 的运行时元数据 | D2 + 收口 |
| `frontend/src/i18n/locales/zh.ts` + `en.ts` | 新增 `newApiPlatform.{models, modelsHint, fetchUpstreamModels, fetchUpstreamModelsHint, fetchUpstreamModelsNeedUrlKey, fetchUpstreamModelsEmpty, fetchUpstreamModelsSuccess, fetchUpstreamModelsFailed}` 8 个 key（zh + en 对称） | D3 |
| `frontend/src/components/account/__tests__/AccountNewApiPlatformFields.spec.ts`（新增） | 6 个 vitest：默认渲染 / 按钮 enable-by-channel-type / disabled 状态 / emit fetch-models / mapping 模式 toggle / 旧 JSON textarea 不再出现 | D3 + D4 |
| `frontend/src/components/account/__tests__/CreateAccountModal.newapi.spec.ts`（新增） | 2 个 vitest：D1 路径 1 验证 / D2 路径 2 验证（不出现重复 base_url label） | D1 + D2 |

### 8.2 取舍记录

1. **`useTkAccountNewApiPlatform` composable 全面启用（2026-04-23 二次重构）**：第一版实现按「最小侵入」把 ~80 行的 newapi 状态 / handler / watcher / 校验 / 提交拼装直接写在 `CreateAccountModal.vue` 与 `EditAccountModal.vue` 里，违反 CLAUDE.md §5「上游大文件保持模板 + wiring」。二次重构把这部分**全部**收口到 `useTkAccountNewApiPlatform.ts`（~280 行 self-contained composable），暴露 ref / computed / 4 个方法（`bootstrap` / `reset` / `populateFromAccount` / `buildSubmitBundle` / `handleFetchUpstreamModels`）。两个 modal 退化为「destructure + v-model 透传 + 4 个调用点」。

   实测结果（与 `upstream/main` 对比）：

   | 文件 | 重构前 vs upstream | 重构后 vs upstream | 减少 |
   | ---- | ------------------- | -------------------- | ---- |
   | `CreateAccountModal.vue` | +294 行 | +136 行 | **−158（−54%）** |
   | `EditAccountModal.vue`   | +362 行 | +232 行 | **−130（−36%）** |
   | 合计 | +656 行 | +368 行 | **−288（−44%）** |

   PR 在交付 D1-D4 体验缺口闭合的同时，**净减少了** 121 行 TK 与 upstream 的偏离（从 +419 降到 +298），未来 `git merge upstream/main` 的冲突面更小。

2. **`model_mapping` raw JSON textarea 移除**：原 textarea + 结构化 selector 同时写入 `credentials.model_mapping`，是双源 bug。结构化 selector（whitelist 模式 = `{model: model}` 镜像 / mapping 模式 = `{from: to}` 重写）覆盖了 100% 的语义；raw JSON 仅对"我想直接粘贴一个奇怪的 mapping JSON"这一极端 power-user 场景有用，**该场景未在本次报告中出现**。textarea 移除后，对应的 `defineModel('modelMapping')` **保留**作为 v-model passthrough，避免父组件破坏；EditModal 仍把 existing model_mapping stringify 给它，但不再渲染。

3. **「获取模型列表」按钮的位置**：放在结构化 selector 下方的 flex 容器内（与上游 new-api `extraText` 位置等价），mapping 模式下也同样显示——因为 fetch 出来的模型列表应该能直接覆盖 selector，所以两种模式都需要按钮（但点击后强制 `restrictionMode = 'whitelist'`，避免拉到 100 个模型却落到 1-1 映射列表里）。

4. **删除真实死代码 `useNewApiChannelTypeModels.ts`**：原本预备给「按 channel_type 自动填充推荐模型」用，但实际方案已经收敛到「让用户主动点『获取模型列表』向上游拉真实列表」（与上游 new-api UX 一致），channel-type 静态预设列表无独立价值。后端路由 `GET /admin/channel-type-models` + 前端 API 客户端 `listChannelTypeModels()` 保留作为契约面，未来需要时再消费。

5. **保留 `useNewApiChannelTypes.ts`**：catalog 加载有进程级缓存 + inflight 共享，被 `ChannelTypeBadge.vue`（账号列表中的徽章渲染）和本 composable 共享消费——属于真实的复用边界，删除会反向把缓存逻辑复制两次。

6. **「测试连接」未在本 PR 范围**：上游 new-api 渠道还有「测试」按钮（POST /api/channel/test/{id}）。TK 已经有独立的 `AccountTestModal`（账号列表行的「⋮」菜单进入），UX 等价但路径不同。本次不动。

### 8.3 验证矩阵实测

| AC | 状态 | 验证方式 |
| -- | ---- | -------- |
| AC-1 (D1) | ✅ | `CreateAccountModal.newapi.spec.ts::AC-D1` |
| AC-2 (D3 channel-type 自动 prefill) | ✅ | `useTkAccountNewApiPlatform.ts::watch(channelType)`；手测：选 channel_type=14 → base_url 自动填 `https://api.deepseek.com` |
| AC-3 (D3 fetch button onClick) | ✅ | `AccountNewApiPlatformFields.spec.ts::emits "fetch-models"` |
| AC-4 (D3 disabled state) | ✅ | `AccountNewApiPlatformFields.spec.ts::disables the 获取模型列表 button` |
| AC-5 (D2 路径 2 不出现重复字段) | ✅ | `CreateAccountModal.newapi.spec.ts::AC-D2` |
| 回归: Anthropic OAuth + setup-token | ✅ | watcher B 的 antigravity / newapi / 默认分支彼此互斥，无新副作用 |
| 回归: Antigravity Upstream | ✅ | 同上；antigravity 分支保持 `accountCategory='oauth-based' + antigravityAccountType='oauth'` |
| 回归: Bedrock | ✅ | 不受影响（type=bedrock 走 watcher A 单独路径） |
| 回归: OpenAI/API Key（不切到 newapi） | ✅ | `accountCategory='apikey'` + `form.platform='openai'` → `form.platform !== 'newapi'`，通用 apikey 块照常渲染 |
| 回归: EditAccountModal 编辑已有 newapi 账号 | ✅ | `populateFromAccount(account)` 一次性 hydrate；composable 自动按 whitelist/mapping 推断初始模式；fetch 按钮在编辑模式可不输入 api_key（走 account_id 路径） |
| 回归: TypeScript 编译 | ✅ | `pnpm typecheck` 通过 |
| 回归: ESLint | ✅ | `pnpm lint:check` 通过 |
| 回归: 前端 build | ✅ | `pnpm build` 通过（dist 输出到 `backend/internal/web/dist/`） |
| 回归: 后端 build | ✅ | `go build ./...` 通过 |
| 回归: dev-rules preflight | ✅ | 段 1-10 全部 pass |
| 回归: TK vs upstream 偏离面 | ✅ | 见 §8.2.1：净减少 121 行（−44% on the two upstream-shaped modal files） |

### 8.4 已知遗留 / 不在本 PR 范围

- 上游 new-api `MODEL_FETCHABLE_TYPES` 包含 type=4 (Ollama)，TK 复用了同一 set。但 Ollama 的 fetch 走 `ollama.FetchOllamaModels(base, key)`，TK 后端已实现（见 `backend/internal/integration/newapi/fetch_upstream_models.go:69-77`）；前端不需要特殊化处理。
- 上游 new-api 渠道还有「Ollama 模型管理」子 modal（type=4 专用），允许在线 pull / delete 模型——这不属于"添加账号"流程的核心 UX，留待 stage-3 backlog。
- 上游 new-api 渠道有「填入相关模型 / 填入所有模型 / 复制所有模型 / 清除所有模型」下拉菜单，TK 暂未做对等下拉——`ModelWhitelistSelector` 已支持手动多选，`获取模型列表` 一键拉真实列表，对核心场景已经够用。

## 9. 致谢与文档卫生

- 本次诊断没有引入任何新 stat 块（`scripts/preflight.sh` 段 8 不需要更新）。
- 本文档已翻 `status: shipped`（PR #47 合并 squash commit `942154d3`，2026-04-23）。
- 如果这条 backlog 后续决定**改方向**（例如把 §8.4 列出的「Ollama 模型管理 / 填入相关模型 / 测试连接」按钮做出来），请保留本文档作为决策链而不是覆盖；新增 `docs/accounts/newapi-add-account-ui-followup-*.md` 续写即可。
