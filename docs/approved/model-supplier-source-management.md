---
title: TokenKey Model Supplier Source Management
status: approved
approved_by: "xuejiao (operator directives through 2026-08-28)"
approved_at: 2026-08-27
updated: 2026-08-30
created: 2026-08-27
owners: [tk-platform]
scope: "supplier facts, admin API/UI, credential isolation, account projection, probe gate, and managed-account ownership"
related_stories: ["US-048"]
---

# TokenKey 模型供应源管理审批基线

## 决策

运营只管理一个对象：供应源。一个供应源表示一个供应商通道、一个 endpoint、一份 API Key、一个
`base_priority` 和一组明确的模型采购事实。保存只写供应源；运营主动点击“校验并同步”后，系统才
把事实投影为现有 TokenKey 账号配置。

线上调度不增加任何供应来源概念。scheduler 继续只比较账号现有 `priority`，数值越小越先调度；
供应源只是按固定规则写出这个字段：

```text
account.priority = source.base_priority + discount_priority
```

所有新供应源默认 `base_priority=100`。采购比例固定分六档，分别增加 `1..6`；`ratio=1.00` 和空值
均进入档位 6。同一供应源、同一档位的模型合并为一个账号，每个供应源最多六个受管账号。
`base_priority` 必须保证再加最大档位值 6 后仍可写入 PostgreSQL `INTEGER`。

原生 OAuth、普通账号和供应账号没有额外调度层。它们的相对顺序完全由各自现有 priority 决定；
供应源不读取或修改原生 OAuth priority。

## 隔离边界

- 第一版只新增 `model_supplier_sources` 一张表，不增加状态、revision、探测历史、同步任务或审计表。
- 供应源不依赖账号组仓储，不读取、比较或写入账号组，不取模型账号组并集，不返回账号组 diff。
  供应投影读取使用专用的无账号组查询，不调用会加载 `AccountGroups`、`GroupIDs` 或 `Groups` 的通用
  `GetByID`。
- 新账号通过现有账号创建服务建立，并显式跳过默认组绑定，保持未分组。普通账号创建仍保持原有失败
  契约。供应源不改变账号网关调度。
- 新账号默认固定为 NewAPI OpenAI Chat API Key transport；当供应源 endpoint 主机为
  `qianfan.baidubce.com` 时，改为 BaiduV2（channel_type=46）并规范化 `base_url` 为
  `https://qianfan.baidubce.com`。结构同步会把 transport 漂移修复回该解析结果。
  账号先以空 `model_mapping`、`schedulable=false` 创建。
- 受管账号和内存预探测账号只声明 Chat Completions。供应源 endpoint 末尾 `/v1` 只在 OpenAI
  受管路径进入 NewAPI OpenAI 适配器前规范化；Qianfan 路径使用 `/v2/chat/completions`。普通
  NewAPI 账号的 base URL 和协议行为保持不变。
- 供应源不读取或写入倍率、售价、pricing registry、计费规则、用户价格或利润数据。
- 除把账号 `status/schedulable` 收敛到目标 mapping 的投影值外，供应源不修改 gateway、scheduler、
  sticky、运行期健康窗口、冷却、限流、fallback、`error_message` 或其他运行期字段。
- 凭证使用最小权限 API Key；不保存供应商后台账号密码，不自动登录、处理 MFA 或代建 API Key。
- API 不回显明文、密文或凭证指纹；日志和 Admin Audit Log 必须擦除请求字段 `credential`。
- 未知或私有协议失败封闭；即使探测得到 Responses、Anthropic Messages 等非 Chat 成功证据，也返回
  `protocol_unsupported`。不按模型前缀猜 transport，也不建设通用协议配置器。
- 凭证 HMAC 指纹复用现有凭证加密密钥的生命周期，不随 JWT secret 轮换；探测日志不记录上游原始响应。

## 保存、预览与同步

“保存”只校验和更新供应源单表，不探测、不建号、不改账号。全局 priority 预览只排序所有供应源
目标档位并提示相同 priority，不扫描普通账号、OAuth 账号或账号组。运营结合账号管理页的真实
priority 自行判断和修改 `base_priority`。已选来源的表单存在未保存修改时，页面禁用“校验并同步”并
提示先保存；同步 API 始终只读取数据库中已保存的供应事实。

“校验并同步”在账号投影前先走只读 `POST /admin/supplier-sources/:id/models-discover`：

1. 拉取上游 models 列表（OpenAI 兼容 `/v1/models`；千帆 BaiduV2 为 `/v2/models`）；
2. 把已配置的上游模型 ID 规整为规范 ID（大小写、空格等）；当 `client_model_id` 与
   `upstream_model_id` 为同一事实（相同或模糊等价）时同步规整 client，显式 remap（如 FMGo
   Seedance）保留 client。无法匹配的保留原值并标 `configured_issues`，不自动删除；
3. 对列表中尚未配置、且 type 可探测（`chat` / `multimodal` / `image2text` / 空）的候选做真实
   Chat Completions 探测；`embeddings` / `text2image` 等直接拒绝建议；
4. **仅探测通过**的候选进入 `suggested_appends`（默认 `purchase_ratio=1.0`）；探测失败的进入
   `rejected_candidates`，不得写入表单建议、更不得投影账号；
5. 若有已配置 ID 规整变更，结果回填表单草稿并要求人工确认后保存；建议追加只展示，需运营主动
   「加入表单」才进入草稿。discover 本身不写供应源、不写账号。保存后再点“校验并同步”才进入
   既有账号投影；
6. 若无需规整确认，即使仍有建议追加，也继续执行下方投影同步（建议可在同屏查看后择机加入）。

同步按以下最小规则执行：

1. 供应商名称、通道名称或 `base_priority` 变化时，只更新受管账号名称和 priority，不探测；
2. endpoint、credential、模型 ID、模型增减、跨档，或非空受管账号的 `status/schedulable` 与目标
   不一致时，先用内存目标账号逐模型真实探测；空 mapping 的调度字段漂移无需探测；
3. 任一模型失败，返回完整当次探测结果且不写账号；全部成功生成仅供本次同步调用链使用的 Chat
   正向证据；
4. 先创建空 mapping、不可调度的新档位账号。非空投影必须携带该证据；service 和 repository 双重
   拒绝未经探测的非空 mapping；
5. 单账号事务同时提交账号配置、仅含 Chat Completions 的现有协议能力、
   `InitialProbeCompleted=true`/`OfficialSeed=false` 的正向证据及普通 scheduler outbox；提交后只读回
   配置确认，不做写后二次网络探测；
6. 再从旧档位移除不再属于它的 mapping；空 mapping 账号保留并设 `schedulable=false`；
7. 任一步失败时返回当前 `failed_step + changes[]`；单账号事务失败时已有账号保持旧配置，新账号保持
   空 mapping、不可调度。跨账号流程保留已完成增加，不执行尚未开始的减少，不做跨账号回滚；重试
   重新计算差异并继续收敛。

不增加 draft/ready/enabled、activate、pause、蓝绿账号、影子 revision、停机屏障或紧急止损状态。页面
在请求期间禁用重复提交；同一来源不支持人工并发编辑或同步，不为低频管理面引入分布式锁或任务编排。

## 账号复用与 ownership

受管账号只增加：

```text
supplier_source_id
supplier_discount_band
```

`supplier_source_id + supplier_discount_band` 是唯一逻辑身份。已有普通账号只有在 endpoint、凭证指纹、
解析后的 NewAPI transport（OpenAI 或 Qianfan 的 BaiduV2）、唯一匹配、单非空档位以及 mapping 子集等窄
条件全部满足时才可接管；账号组不参与匹配，接管前后保持不变。匹配查询覆盖全部未删除 NewAPI 账号以
识别凭证冲突；只有当前 `status=active` 的唯一精确匹配可自动接管，`disabled/error` 精确匹配返回冲突，
不绕过它新建重复账号。

存在 `supplier_source_id` 的账号，其供应源拥有字段只能由供应源同步服务通过专用账号命令写入。所有
通用账号配置写入口和 CRS 导入覆盖在 service 层整体拒绝，普通创建和导入也拒绝伪造保留 Extra；
通用 credentials 刷新同样拒绝。供应投影更新必须走不携带 `rate_multiplier` 的窄写路径，不能回退到
通用账号 Update。恢复错误、重置配额、代理 fallback 和账号探测等运行期专用操作继续允许。

账号列表、详情和操作菜单显眼显示：

```text
供应源托管 · {supplier_name}/{channel_name}
```

徽标链接携带 `source_id`，供应源页面加载列表后直接选中对应来源；账号页重新挂载时刷新名称目录，
避免同一会话内供应源改名后仍显示旧名称。

普通编辑、复制、删除、状态/可调度切换和通用批量写显示统一原因：

```text
该账号由供应源托管，请前往供应源管理修改。
```

账号组仍在账号组管理入口独立维护。

## 第一版 API 与页面

```text
GET    /admin/supplier-sources
GET    /admin/supplier-sources/:id
POST   /admin/supplier-sources
PUT    /admin/supplier-sources/:id
GET    /admin/supplier-sources/priority-preview
POST   /admin/supplier-sources/:id/models-discover
POST   /admin/supplier-sources/:id/sync
```

不提供 DELETE、validate、activation-preview、activate、pause 或供应源 audits API。页面只展示运营
字段、档位与目标 priority、全局供应源 priority、保存、单源“校验并同步”（内嵌 models-discover
规整/建议）、当次发现结果、当次逐模型探测结果和实际账号变化。

## 首批验收

首批三个案例必须各自提供完整、准确的供应事实才能同步。不匹配时不扩大已有账号匹配，不改网关调度。

- 佳杰 / VSTECS：首批只录入 `deepseek-v4-pro` 与 `qwen-3.7-max` 的最低合法比例 `0.50`，两者进入档位
  3 并合并到一个目标账号。当前没有生产 API Key，因此真实 HTTP 探测和账号同步必须保持 `not_run`，
  要求补全真实 endpoint 与凭证后再验收。
- FMGo：只录入客户 ID `doubao-seedance-2-0-260128` 到上游 ID
  `feimiao-seedance-2-0-260128` 的显式映射，ratio `0.50`。当前缺少 endpoint 与凭证；固定边界返回
  `protocol_unsupported`，不发起伪 OpenAI Chat 请求，也不写账号。这不是通用前缀替换。
- 百度千帆：供应源 endpoint 主机为 `qianfan.baidubce.com` 时使用 BaiduV2 transport，可接管生产账号
  90（channel_type=46、同一凭证与 endpoint 根）。缺少供应凭证时仍无法完成指纹匹配或同步；凭证齐备后
  Sync 走 Chat Completions 探测与投影，不再因 transport 固定为 OpenAI 而 `protocol_unsupported`。

完整字段、边界和验收标准以本文为实现依据。
