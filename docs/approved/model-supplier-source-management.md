---
title: TokenKey Model Supplier Source Management
status: approved
approved_by: "xuejiao (operator directives through 2026-09-03)"
approved_at: 2026-08-27
updated: 2026-09-03
created: 2026-08-27
owners: [tk-platform]
scope: "supplier facts, admin API/UI, credential isolation, account projection, probe gate; managed accounts behave like ordinary accounts after create, with sync overwriting projection fields"
related_stories: ["US-048"]
revision_note: >
  2026-09-03: Project writes managed accounts only and never probes;
  validate remains the sole configured-model probe. Same day: supplier
  identity SSOT — row unique key is (supplier_name, endpoint,
  credential_fingerprint, channel_type); DB/API field channel_name
  renamed to supplier_lane (display label only). Adoption candidates
  filter by channel_type transport. 2026-09-02: Anthropic
  channel_type=14 messages-only exception.
---

# TokenKey 模型供应源管理审批基线

> 按钮与探测主路径以 [`model-supplier-source-probe-sync-split.md`](model-supplier-source-probe-sync-split.md) 为准：发现模型 -> 保存 -> 校验模型 -> 投影账号。FMGo Seedance 改写见 [`model-supplier-source-fmgo-seedance-account-rewrite.md`](model-supplier-source-fmgo-seedance-account-rewrite.md)。

## 决策

运营只管理一个对象：供应源。一个供应源表示一个供应商、一个 **TokenKey 协议类型**（`channel_type`）、
一个规范化 endpoint、一份 API Key、一个 `base_priority` 和一组明确的模型采购事实。字段
`supplier_lane` 仅是供应商侧通道标签（供应通道名 / supplier lane），用于展示与分组，**不参与**
行身份或接管冲突判定。保存只写供应源；运营主动点击“投影账号”后，系统才把事实投影为现有
TokenKey 账号配置。

线上调度不增加任何供应来源概念。scheduler 继续只比较账号现有 `priority`，数值越小越先调度；
供应源只是按固定规则写出这个字段：

```text
account.priority = source.base_priority + discount_priority
```

所有新供应源默认 `base_priority=100`。采购比例固定分六档，`discount_priority` 分别为
`10, 20, …, 60`（相邻档位增量 10）；`ratio=1.00` 和空值均进入档位 6。同一供应源、同一档位的
模型合并为一个账号，每个供应源最多六个受管账号。`base_priority` 必须保证再加最大档位贡献 60
后仍可写入 PostgreSQL `INTEGER`。

原生 OAuth、普通账号和供应账号没有额外调度层。它们的相对顺序完全由各自现有 priority 决定；
供应源不读取或修改原生 OAuth priority。

## 隔离边界

- 第一版只新增 `model_supplier_sources` 一张表，不增加状态、revision、探测历史、同步任务或审计表。
- 供应源不依赖账号组仓储，不读取、比较或写入账号组，不取模型账号组并集，不返回账号组 diff。
  供应投影读取使用专用的无账号组查询，不调用会加载 `AccountGroups`、`GroupIDs` 或 `Groups` 的通用
  `GetByID`。
- 新账号通过现有账号创建服务建立，并显式跳过默认组绑定，保持未分组。普通账号创建仍保持原有失败
  契约。供应源不改变账号网关调度。
- 供应源持久化 `channel_type`（Extension Engine 枚举）；Admin 表单必选类型，endpoint 主机仍可作
  兼容提示，但 transport / models 路径 / 受管账号投影以 `channel_type` 为准。默认 OpenAI
  Chat（1）；千帆 BaiduV2（46）规范化 `base_url` 为 `https://qianfan.baidubce.com`；DashScope Ali
  （17）使用 `compatible-mode/v1/*` 路径；Anthropic（14）走原生 Messages（`/v1/messages`），用于
  CloudWise 等上游仅 messages 可服务 Claude opus/sonnet 的供应源。结构同步会把 transport 漂移
  修复回该解析结果。账号先以空 `model_mapping`、`schedulable=false` 创建。
- 受管账号和内存预探测账号按通道声明**单一**协议能力：默认仅 Chat Completions；视频通道（54）走
  视频方言；Anthropic（14）仅声明 Messages。供应源 endpoint 末尾 `/v1` 只在 OpenAI 受管路径进入
  NewAPI OpenAI 适配器前规范化；Qianfan 路径使用 `/v2/chat/completions`；Anthropic 路径使用
  `/v1/messages`。普通 NewAPI 账号的 base URL 和协议行为保持不变。
- 供应源不读取或写入倍率、售价、pricing registry、计费规则、用户价格或利润数据。
- 除把账号 `status/schedulable` 收敛到目标 mapping 的投影值外，供应源不修改 gateway、scheduler、
  sticky、运行期健康窗口、冷却、限流、fallback、`error_message` 或其他运行期字段。供应源拥有
  `account_concurrency`（默认 `1000`），同步时写入受管账号 `concurrency`，不再保留账号页手工设置的
  并发值。
- 凭证使用最小权限 API Key；不保存供应商后台账号密码，不自动登录、处理 MFA 或代建 API Key。
- API 不回显明文、密文或凭证指纹；日志和 Admin Audit Log 必须擦除请求字段 `credential`。
- 探测协议由 `channel_type` 决定，不按模型前缀猜 transport，也不建设通用协议配置器。默认只接受
  Chat Completions 正向证据；视频通道走视频方言；Anthropic（14）只接受 Messages 正向证据。其它
  协议成功证据（含 Anthropic 通道上的 Chat、Chat 通道上的 Messages/Responses）一律
  `protocol_unsupported`。
- 凭证 HMAC 指纹复用现有凭证加密密钥的生命周期，不随 JWT secret 轮换；探测日志不记录上游原始响应。

## 保存、预览与同步

“保存”只校验和更新供应源单表，不探测、不建号、不改账号。全局 priority 预览只排序所有供应源
目标档位并提示相同 priority，不扫描普通账号、OAuth 账号或账号组。运营结合账号管理页的真实
priority 自行判断和修改 `base_priority`。已选来源的表单存在未保存修改时，页面禁用发现、校验与投影并
提示先保存；投影 API 始终只读取数据库中已保存的供应事实。

发现、保存、校验、投影的按钮顺序、禁用矩阵与探测主路径以
[`model-supplier-source-probe-sync-split.md`](model-supplier-source-probe-sync-split.md) 为准。
`POST .../discover` 只读发现候选，`POST .../validate` 只读校验已配置模型，保存只写供应源；
`POST .../sync` 只投影账号，不探测。Validate 结果不缓存也不授权后续写入。
旧 `POST .../probe` 与 probe job 路由保留为 Discover 兼容别名；管理路由不再注册 `models-discover`。

上游列表仍按通道能力拉取（OpenAI 兼容 `/v1/models`；千帆 BaiduV2 为 `/v2/models`；DashScope Ali
为 `/compatible-mode/v1/models`；Anthropic 14 仍用 `/v1/models` 发现）。探测协议由通道能力决定
（`channel_type=54` 走视频门禁；`channel_type=14` 走 Messages），不按 client 名字开例外。失败时
API 必须返回可读 `message` 与 `failed_step`；管理页必须在结果区之外独立展示错误文案，禁止只露出
空标题。

同步按以下最小规则执行：

1. 供应商名称、`supplier_lane`（供应通道名）或 `base_priority` 变化时，只更新受管账号名称和 priority，不探测；
1b. `account_concurrency` 变化时，只更新受管账号 `concurrency`，不探测；
2. endpoint、credential、模型 ID、模型增减、跨档，或非空受管账号的 `status/schedulable` 与目标
   不一致时，直接按目标 mapping 投影，不探测；空 mapping 的调度字段漂移同样不探测；
3. 非空投影按 `channel_type` 声明单一协议能力（默认 Chat；Anthropic 14 为 Messages；视频 54 为视频方言）；
4. 先创建空 mapping、不可调度的新档位账号。非空投影必须携带该通道协议声明；service 和 repository 双重
   拒绝未声明协议能力的非空 mapping；
5. 单账号事务同时提交账号配置、该通道单一协议能力（默认 Chat Completions；Anthropic 仅 Messages）、
   `InitialProbeCompleted=true`/`OfficialSeed=false` 的正向证据及普通 scheduler outbox；提交后只读回
   配置确认，不做写后二次网络探测；
6. 再从旧档位移除不再属于它的 mapping；空 mapping 账号保留并设 `schedulable=false`；
7. 任一步失败时返回当前 `failed_step + changes[]`；单账号事务失败时已有账号保持旧配置，新账号保持
   空 mapping、不可调度。跨账号流程保留已完成增加，不执行尚未开始的减少，不做跨账号回滚；重试
   重新计算差异并继续收敛。

不增加 draft/ready/enabled、activate、pause、蓝绿账号、影子 revision、停机屏障或紧急止损状态。页面
在请求期间禁用重复提交；同一来源不支持人工并发编辑或同步，不为低频管理面引入分布式锁或任务编排。

## 身份 SSOT（三层，各一 owner）

```text
① 供应源行身份（model_supplier_sources）
   UNIQUE (supplier_name, endpoint, credential_fingerprint, channel_type)
   supplier_lane = 供应通道名（展示标签），改名不改身份

② 投影/冲突候选（FindCredentialEndpointMatches）
   fingerprint + endpoint + channel_type（transport 兼容）
   不兼容协议类型的账号永不进入候选；其它源同 transport 托管账号 → IdentityConflict

③ 受管账号身份（accounts.extra）
   supplier_source_id + supplier_discount_band
```

同供应商、同 endpoint、同凭证、**不同 `channel_type`**（例如 OpenAI chat=1 与 Anthropic
messages=14）是合法并行供应源，各自投影自己的受管账号。

## 账号复用与 ownership

受管账号只增加：

```text
supplier_source_id
supplier_discount_band
```

`supplier_source_id + supplier_discount_band` 是受管账号唯一逻辑身份。已有**未托管**普通账号只有在
endpoint、凭证指纹、**同一 `channel_type` transport**、唯一匹配、单非空档位以及 mapping 子集等窄
条件全部满足时才可接管；账号组不参与匹配，接管前后保持不变。匹配查询只返回与本源 transport
兼容的未删除 NewAPI 账号；只有当前 `status=active` 的唯一精确匹配可自动接管，`disabled/error`
精确匹配返回冲突，不绕过它新建重复账号。其它供应源已托管且 transport 相同的账号一律
`IdentityConflict`；transport 不同则视为无关身份，不阻挡本源新建投影。

受管账号创建后，在账号管理页按**普通账号**对待：可编辑、删除、复制、切换调度、批量更新等。
普通创建与导入仍拒绝伪造 `supplier_source_id` / `supplier_discount_band`；普通 Extra 编辑会保留
已有受管身份键，复制账号时剥离这些键以免重复身份。

仅在供应源点击「投影账号」时，通过专用窄写命令覆盖供应源投影字段（名称、凭证含
`model_mapping`、priority、concurrency、status/schedulable、transport 等），不读取或写入账号组，
也不携带 `rate_multiplier`。供应投影更新不能回退到通用账号 Update。

`model_mapping` 所有权：带 `extra.supplier_source_id` 的账号由供应源 Sync 拥有（采购投影的精确
集合）。`ops/pricing/manage-account-model-mapping-runtime.py` 的 `check-accounts` /
`apply-accounts` / `release-gate` **不得**用平台/渠道 serving floor 去扩写或判缺这些账号；
floor 收敛只作用于非供应源托管账号。否则会出现 Sync 子集 ↔ floor 全量的乒乓。

账号列表显示徽标：

```text
供应源托管 · {supplier_name}/{supplier_lane}
```

徽标链接携带 `source_id`，供应源页面加载列表后直接选中对应来源。账号编辑提示说明：平时可按普通
账号编辑；「投影账号」会覆盖投影字段。

## 第一版 API 与页面

```text
GET    /admin/supplier-sources
GET    /admin/supplier-sources/:id
POST   /admin/supplier-sources
PUT    /admin/supplier-sources/:id
GET    /admin/supplier-sources/priority-preview
POST   /admin/supplier-sources/:id/probe
GET    /admin/supplier-sources/:id/probe/jobs/:job_id
POST   /admin/supplier-sources/:id/discover
GET    /admin/supplier-sources/:id/discover/jobs/:job_id
POST   /admin/supplier-sources/:id/validate
POST   /admin/supplier-sources/:id/sync
```

`probe` 与 probe job 是 Discover 的兼容别名。不提供 DELETE、activation-preview、activate、pause、供应源 audits 或 `models-discover` API。供应源读写体含
`account_concurrency`（正整数，默认 `1000`）。页面只展示运营
字段、档位与目标 priority、全局供应源 priority、发现、校验、保存、投影、当次发现结果、
当次逐模型探测结果和实际账号变化。

## 首批验收

首批三个案例必须各自提供完整、准确的供应事实才能同步。不匹配时不扩大已有账号匹配，不改网关调度。

- 佳杰 / VSTECS：首批只录入 `deepseek-v4-pro` 与 `qwen-3.7-max` 的最低合法比例 `0.50`，两者进入档位
  3 并合并到一个目标账号。当前没有生产 API Key，因此真实 HTTP 探测和账号同步必须保持 `not_run`，
  要求补全真实 endpoint 与凭证后再验收。
- FMGo：只录入官方 Seedance 2.0 / 2.0-fast / 2.5 remap（锚点 `feimiao-v2-431-720p-15s` /
  `feimiao-v2-431-fast-720p-15s` / `feimiao-v2.5-720p-15s`）。`channel_type=54` 走视频门禁，不以 OpenAI Chat 伪探测冒充成功。
  运行时 SKU 改写见 [`model-supplier-source-fmgo-seedance-account-rewrite.md`](model-supplier-source-fmgo-seedance-account-rewrite.md)。
- 百度千帆：供应源 endpoint 主机为 `qianfan.baidubce.com` 时使用 BaiduV2 transport，可接管生产账号
  90（channel_type=46、同一凭证与 endpoint 根）。缺少供应凭证时仍无法完成指纹匹配或同步；凭证齐备后
  Sync 走 Chat Completions 探测与投影，不再因 transport 固定为 OpenAI 而 `protocol_unsupported`。

完整字段、边界和验收标准以本文为实现依据。
