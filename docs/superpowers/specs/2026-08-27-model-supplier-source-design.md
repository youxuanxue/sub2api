# TokenKey 模型供应源管理设计

## 1. 决策摘要

供应源是一个低频管理面工具：运营保存供应事实，确认后将其同步成 TokenKey 现有账号配置。

```text
model_supplier_sources
        ↓ 单源校验并同步
accounts / account_groups
        ↓
现有 TokenKey scheduler
```

线上调度器不读取供应源表，不识别供应商、采购比例或折扣档位，只继续使用现有账号字段和
`priority` 语义。

本设计明确不引入：

- `native → supplier → unmanaged` 调度层；
- `tk_source_class` 或原生账号回填；
- 供应源状态机；
- 独立供应模型表、探测历史表或供应源审计表；
- 逐模型执行账号；
- 蓝绿账号、影子 revision、全量回滚或发布编排；
- 与倍率、售价、定价或计费的任何关联。

## 2. 目标与隔离边界

运营只管理：从哪个供应通道、使用哪份 API Key、供应哪些明确模型、采购比例是多少，以及该通道
在全局账号 priority 中的人工基础优先级。

系统负责：

1. 保存采购事实；
2. 将采购比例归入固定折扣档；
3. 计算账号最终 priority；
4. 按折扣档生成最多六个受管账号；
5. 在同步前真实探测目标模型；
6. 按先增后减更新 `model_mapping`；
7. 复用现有账号更新与调度缓存刷新机制热更新。

本设计不：

- 修改 gateway、scheduler、sticky、健康、冷却、限流或 fallback 逻辑；
- 读取或写入 `rate_multiplier`；
- 读取或写入模型售价、pricing registry、渠道定价或用户计费规则；
- 根据采购比例推导售价、利润或倍率；
- 把供应源字段暴露到用户 API；
- 自动猜测上游协议或模型 ID；
- 在保存供应源时修改线上账号；
- 建设供应商、合同、容量或稳定性评分等额外管理实体。

## 3. 唯一管理对象

唯一管理对象是“供应源”。一个供应源代表：

```text
一个供应商通道
+ 一个 endpoint
+ 一份 API Key
+ 一个 base_priority
+ 一组明确采购模型
```

以下任一项不同，就应建立不同供应源：

- endpoint；
- API Key；
- channel；
- 实际需要独立管理的合同通道。

同一供应商可以有多个供应源。不同供应商必然是不同供应源，因此不会共享受管账号。

`base_priority` 属于具体供应源，而不是供应商主数据。同一供应商的不同 endpoint、凭证或合同通道
可以有不同基础优先级。

## 4. 数据契约

第一版只新增一张管理表：

```text
model_supplier_sources
  id
  supplier_name
  channel_name
  endpoint
  encrypted_credential
  base_priority
  models JSONB
  notes
  created_at
  updated_at
```

不增加 `state`、`enabled`、`revision`、`applied_revision`、探测状态或投影账号 ID。

### 4.1 运营字段

```text
supplier_name          供应商名称
channel_name           供应通道或合同模型组
endpoint               上游基地址
credential             API Key，保存后不回显
base_priority          默认 100，允许运营填写任意整数
models[]               明确采购模型
notes                  开放备注，不参与运行判断
```

所有新供应源的 `base_priority` 默认都是 `100`。系统不根据供应商名称、价格、错误率或账号类型自动
调整该值。运营掌握稳定性事实后自行修改。

系统不限制 `base_priority` 为 100 的倍数，也不限制其必须大于等于 100。priority 重叠、交叉或
供应账号排在其他账号之前，均由运营在全局预览中判断。

### 4.2 模型字段

`models` 使用 JSONB 保存采购事实：

```json
[
  {
    "client_model_id": "deepseek-v4-pro",
    "upstream_model_id": "deepseek-v4-pro",
    "purchase_ratio": 0.5
  }
]
```

每个模型项只有：

```text
client_model_id        TokenKey 客户模型 ID
upstream_model_id      上游实际接受的模型 ID
purchase_ratio         可空；非空时必须满足 0 < ratio <= 1
```

约束：

- `client_model_id` 在同一供应源内唯一；
- 禁止普通 `*`、“全系列”或其他模糊模型表达；
- `upstream_model_id` 必须逐行明确；
- `43折`、`55折` 等自由文本不能替代数值比例；
- 允许空模型清单，表示该供应源当前没有目标供给。

### 4.3 凭证

供应源使用现有加密能力保存 API Key。凭证仅供管理面探测和账号同步使用：

- API 不回显明文或密文；
- 编辑已有供应源时，凭证留空表示沿用现有值，只有填写新值才轮换；
- 日志与 Admin Audit Log 不记录凭证明文；
- 同步时将凭证复制到账号现有 credentials 结构；
- gateway 仍只读取账号凭证，不读取供应源表；
- 不支持通用后台账号密码登录、MFA、验证码或自动创建 API Key。

## 5. 固定折扣档与 priority

采购比例只用于归入系统固定折扣档。折扣档不会进入线上调度器；它只参与生成账号最终
`priority`。

```text
档位 1：0 < ratio < 0.20       discount_priority = 1
档位 2：0.20 <= ratio < 0.40   discount_priority = 2
档位 3：0.40 <= ratio < 0.60   discount_priority = 3
档位 4：0.60 <= ratio < 0.80   discount_priority = 4
档位 5：0.80 <= ratio < 1.00   discount_priority = 5
档位 6：ratio = 1.00 或为空    discount_priority = 6
```

账号最终 priority：

```text
account.priority = source.base_priority + discount_priority
```

例如：

```text
供应源 A：base_priority 100，ratio 0.50 → priority 103
供应源 B：base_priority 100，ratio 0.70 → priority 104
供应源 C：base_priority 200，ratio 0.10 → priority 201
```

TokenKey 原生 OAuth 可以继续使用现有较小 priority，例如 `1`，但供应源模块不读取、标记、修改或
强制保护原生账号 priority。

不同供应源可能生成相同 priority。相同 priority 后如何选择，完全交给 TokenKey 现有健康、负载、
粘性和同优先级调度规则。

## 6. 档位账号投影

同一供应源、同一折扣档位的模型合并到一个受管账号：

```text
供应源
├── 档位 1 账号：该档全部模型 mapping
├── 档位 2 账号：该档全部模型 mapping
...
└── 档位 6 账号：该档全部模型 mapping
```

因此每个供应源最多生成六个账号，账号数量取决于折扣档数量，而不是模型数量。

受管账号 Extra 只增加：

```text
supplier_source_id
supplier_discount_band
```

不增加：

- `tk_source_class`；
- 供应商调度类别；
- 供应模型行 ID；
- credential fingerprint；
- `create|bind` 状态；
- 探测或同步状态。

每个档位账号保存该档全部模型的完整 `model_mapping`。档位最后一个模型被移走后：

```text
model_mapping 为空
→ schedulable=false
→ 保留账号和账号 ID
→ 以后该档再次出现模型时复用
```

不自动删除、归档或复制受管账号。

`supplier_source_id + supplier_discount_band` 是档位账号的唯一逻辑身份。同步时：没有匹配账号就创建，
唯一匹配就复用，匹配到多个就停止并返回冲突；系统不猜测哪个账号正确。

## 7. 保存与全局预览

### 7.1 保存

“保存”只更新 `model_supplier_sources`：

- 校验并规范化运营字段；
- 加密保存新凭证；
- 更新 base priority、模型 JSON 和备注；
- 不创建、更新、停用或删除账号；
- 不执行上游探测。

供应源配置与真实账号不一致时，管理页通过现场对比显示“待同步”；这只是派生展示，不是数据库状态。

### 7.2 全局 priority 预览

管理页计算所有供应源各折扣档位的最终 priority，并与受影响模型的现有账号一起排序展示。

以下情况只提示，不阻断保存或同步：

- 不同供应源计算出相同 priority；
- 多个供应源的 priority 区间交叉；
- 供应账号排在 OAuth 或普通账号之前；
- 普通账号排在供应账号之前。

页面不将这些提示解释成新的调度规则。运营确认最终数字，线上 scheduler 只消费最终账号 priority。

全局页面只提供单源同步，不提供“全部供应源同步”。

## 8. 校验并同步

“校验并同步”按钮本身就是确认操作。系统读取数据库中该供应源的最新保存配置，不保存 preview、
ready 状态或确认 revision。

同一供应源的保存与同步请求按供应源 ID 串行执行，避免两个同步同时创建档位账号，或保存与同步互相
覆盖。该互斥只存在于请求执行期间，不落数据库状态，也不是同步任务或状态机。

### 8.1 计算目标投影

系统按固定折扣档计算：

- 最多六个目标档位账号；
- 每个账号完整 `model_mapping`；
- endpoint 与 API Key；
- 目标模型在现有 TokenKey 配置中已经可用的正式账号组；
- `base_priority + discount_priority`。

供应源不创建模型目录、账号组或价格配置。同步前若 `client_model_id` 尚未进入 TokenKey 已有模型与
正式组配置，返回 `client_model_not_ready`，不探测上游、不写账号。档位账号绑定其目标模型对应正式
组的并集。

变更按其实际账号投影分类：

- 只修改供应商名称、通道名称、备注，或采购比例变化后仍在同一折扣档：账号投影不变，同步直接
  返回无变化，不探测模型；
- 仅 `base_priority` 变化：只更新该供应源现有档位账号 priority，不探测模型；
- endpoint、credential、模型 ID、模型增减或折扣档发生变化：属于结构变化。

正式账号组由现有配置现场派生；只有组绑定发生变化时，更新组绑定但不因此单独重复探测上游。

### 8.2 现场探测

结构变化时，系统使用目标 endpoint、凭证、transport 和明确 upstream model ID 现场探测全部目标
模型。

- 任一模型失败：返回本次逐模型结果，不写任何账号；
- 全部模型成功：立即进入账号同步；
- 空模型清单：无需探测，直接收敛为空投影。

探测结果只存在于本次 API 响应和页面当前结果中，不写供应源表，不建设探测历史。现有 Admin Audit
Log 只记录操作人、时间、供应源 ID 以及操作成功或失败，不记录凭证或上游原始正文。

当次结果至少包含：

```text
client_model_id
upstream_model_id
折扣档位
成功或失败
脱敏错误分类
简短运营建议
```

### 8.3 先增后减

所有目标模型探测成功后，系统按以下顺序执行普通账号写入：

1. 创建不存在的档位账号；
2. 写入目标 endpoint、credential、正式组和最终 priority，并先向目标档位账号增加新的
   `model_mapping`；
3. 确认仍在供应源目标清单中的每个模型都已存在于目标档位账号；
4. 再从旧档位账号删除不再属于该档的 mapping；
5. 将 mapping 为空的账号设为不可调度；
6. 返回账号新增、mapping 和 priority 的实际变化。

采购比例跨档时，例如模型从档位 3 移到档位 2：

```text
先给档位 2 账号增加模型
→ 再从档位 3 账号删除模型
```

本设计不暂停整个供应源，不建设候选版本、蓝绿账号、调度快照屏障或严格零停机协议。先增后减只是
用最小复杂度降低同步过程中的不可用概率。

先增后减只适用于仍在目标清单、但需要在档位账号之间移动的模型；从供应源目标清单中删除的模型
没有新的目标账号，直接按目标投影移除。

endpoint 或 credential 修改在现场探测成功后，直接原地更新相关受管账号，不创建蓝绿替代账号。
此时步骤 2 写入的临时 mapping 为“当前 mapping 中仍在本源目标清单的部分”与“本档目标 mapping”
的并集，并与新 endpoint、credential 在同一次账号更新中写入。这样不会把已经从供应源删除、未经
新配置探测的模型带到新 endpoint；跨档模型在步骤 4 再被裁剪。跨账号写入仍不保证原子性，过程中
允许短暂重复供给。

### 8.4 失败与重试

同步开始写账号后发生错误：

- 立即停止后续步骤；
- 保留已经成功的增加操作；
- 不再执行尚未开始的减少操作；
- 不做跨账号快照回滚；
- 返回已完成步骤和失败位置；
- 再次同步时重新计算目标与实际差异，幂等继续收敛。

先增后减下，中途失败优先留下重复供给，而不是主动造成模型没有供给。系统不承诺一次同步跨多个
账号的数据库原子性。

## 9. 已有账号复用

第一版仅在以下条件全部满足时自动复用普通已有账号：

1. 规范化 endpoint 完全一致；
2. API Key 完全一致；
3. 账号 platform/transport 与第一版支持的 NewAPI OpenAI-compatible Chat 路径一致；
4. 只匹配到一个已有账号；
5. 供应源当前只有一个非空折扣档位；
6. 已有账号的每条 `model_mapping` 都在供应源目标 mapping 中完全相同，供应源可以新增 mapping，
   但不能接管含有目标清单外 mapping 的账号；
7. 该账号尚未受其他供应源管理。

匹配只在同步时执行，保存供应源不查找或修改账号。

复用成功时，账号写入 `supplier_source_id` 和 `supplier_discount_band`，从此由该供应源完整拥有第 11
节列出的字段；这不是临时绑定。

任一条件不满足时停止同步并返回冲突账号，不自动选择、合并、拆分或覆盖已有账号。百度账号 90
属于该窄路径的首批验收案例。

## 10. 协议边界

运营不选择 TokenKey platform、channel type 或协议。

供应源的 `channel_name` 只是供应商合同通道名称，不是 TokenKey 的 platform、transport 或协议配置。

第一版只支持：

1. 新建档位账号固定使用 NewAPI OpenAI-compatible Chat API Key transport；
2. 窄条件复用已有账号时沿用其已经匹配成功的同类 transport。

不支持 Responses、Anthropic Messages、Embedding、图片、视频或任意私有异步协议的自动发现。
无法通过上述固定路径探测时返回 `protocol_unsupported`，不写账号。

以后增加协议必须通过明确代码适配器和真实验收，不建设运营可配置的协议猜测器。

FMGo 首批只保存 Seedance 采购信息：

```text
client_model_id   = doubao-seedance-2-0-260128
upstream_model_id = feimiao-seedance-2-0-260128
```

`doubao-` 与 `feimiao-` 是逐行显式映射，不是通用前缀替换规则。在视频适配器完成前，FMGo 同步返回
`protocol_unsupported`，不创建或修改任何账号。

## 11. 账号所有权与普通账号入口

存在 `supplier_source_id` 的账号是供应源受管账号。供应源拥有：

- endpoint 和 API Key；
- `model_mapping`；
- priority；
- 正式分组；
- schedulable。

普通账号页面可以查看受管账号，但不能：

- 修改上述受管字段；
- 直接修改 Extra；
- 复制或删除账号。

账号运行期健康、错误、冷却、限流、最后使用时间等继续由现有 TokenKey 机制管理。供应源不引入
capacity、expandability、共享额度或稳定性评分字段。

供应源页面不提供启用、暂停、紧急止损、删除或归档操作。需要停止某个供应源供给时，运营清空
模型清单并执行同步；空档位账号会变为不可调度。

## 12. 管理 API 与页面范围

第一版 API 只保留：

```text
GET    /admin/supplier-sources
GET    /admin/supplier-sources/:id
POST   /admin/supplier-sources
PUT    /admin/supplier-sources/:id
GET    /admin/supplier-sources/priority-preview
POST   /admin/supplier-sources/:id/sync
```

不提供 DELETE、validate、activation-preview、activate、pause 或供应源 audits API。

供应源管理页面只展示：

- 运营字段编辑；
- 全局 priority 排序和非阻断提示；
- 当前供应源目标配置与真实账号差异；
- 单源保存；
- 单源校验并同步；
- 当前同步请求的逐模型结果与账号变化。

页面不展示或管理倍率、售价、计费、协议、capacity、并发、账号技术状态机或探测历史。

## 13. 首批验收案例

### 13.1 佳杰 / VSTECS

首批只录入运营确认的同模型系列最低合法采购比例：

```text
deepseek-v4-pro  ratio 0.50 → 档位 3
qwen-3.7-max     ratio 0.50 → 档位 3
```

两者属于同一供应源、同一折扣档，因此合并到一个档位 3 账号。系统使用明确 endpoint、API Key 和
upstream model ID 逐模型探测；全部成功后才创建或更新账号。

没有真实 API Key 时只能保存采购信息，不能完成同步验收。

### 13.2 FMGo

FMGo Seedance 使用显式双 ID，并归入 ratio 0.50 的档位 3。由于第一版没有视频适配器，校验并同步
必须返回 `protocol_unsupported`，且不写账号。

### 13.3 百度千帆

当账号 90 与供应源 endpoint、API Key、transport 唯一一致，且本源只有一个非空折扣档位、mapping
兼容时，系统复用账号 90，而不是创建重复账号。任一窄条件不满足时停止，不猜测。

## 14. 验收标准

- 部署供应源代码不会改变 scheduler 或现有账号 priority。
- 保存供应源不会创建、修改、停用或删除任何账号。
- 所有新供应源 `base_priority` 默认 100，运营可以填写任意整数。
- 六个折扣档边界正确，`1.00` 和空比例都进入档位 6。
- 同一供应源、同一折扣档的模型合并到一个账号。
- 每个供应源最多六个受管账号。
- 最终账号 priority 严格等于 `base_priority + discount_priority`。
- priority-only 同步不执行上游模型探测。
- 结构同步中任一模型探测失败时不写任何账号，并返回当次逐模型结果。
- 写入顺序先增加 mapping，后删除 mapping。
- 写入中途失败保留已完成的增加操作，不执行后续减少；重试可以幂等收敛。
- 空档位账号不可调度但继续保留复用。
- 只有窄条件唯一匹配才能自动复用已有账号。
- 新建账号只支持 NewAPI OpenAI-compatible Chat；未知协议失败封闭。
- 受管账号不能从普通账号入口修改、复制或删除。
- 供应源表、API、日志和 Admin Audit Log 不泄露凭证或上游原始响应正文。
- 供应源功能不读取或写入倍率、价格、定价和计费数据。
- 佳杰、FMGo 和百度案例必须留下真实请求或真实线上账号证据；mock 只验证页面编排。

## 15. 实现删除与迁移边界

当前供应源实现和 migration 尚未上线，因此直接重写当前草案，不兼容被否决的旧设计。

实现时必须删除：

- gateway scheduler 中的供应来源分层比较；
- `SupplierRoutingLayer` 和 `tk_source_class`；
- 原生账号 manifest、回填脚本及相关 preflight；
- OAuth import 和 edge relay 写入 native 标记的逻辑；
- `model_supplier_source_models` 与 `model_supplier_source_audits`；
- 状态、revision、探测证据和 activation/pause 服务；
- activation preview、专用 audits 和状态操作 API；
- 逐模型账号、优先级全池重排和跨账号快照回滚。

生产 migration 只新增一张供应源管理表和必要索引：

- 不修改现有账号；
- 不回填账号 Extra；
- 不重排 priority；
- 不触发账号进入调度；
- 回滚旧镜像时旧服务可以忽略该表。

只有运营主动同步某个供应源时，才会修改该源最多六个受管账号。线上影响被限制为：

```text
单个供应源
× 最多六个折扣档位账号
× 供应源明确声明的模型
```

## 16. 实现范围控制

第一版只实现：

- 单表供应源创建、查询和更新（无删除）；
- 加密 API Key；
- models JSONB；
- 固定六档采购比例；
- 全局 priority 预览；
- 单源校验并同步；
- NewAPI OpenAI-compatible Chat 探测；
- 窄条件复用已有账号；
- 每档一个账号；
- 先增后减、失败停止和幂等重试；
- 受管账号普通入口保护。

第一版不实现：

- scheduler 供应来源层；
- 原生 OAuth 标记或 priority 管理；
- 全部供应源批量同步；
- 状态机、启用、暂停、紧急止损、删除或归档；
- 探测历史、供应源专用审计或同步任务表；
- 蓝绿账号、候选版本、严格零停机或全量回滚；
- 多账号冲突选择、通用账号拆分或合并；
- Responses、Anthropic、Embedding、图片或视频自动适配；
- capacity、共享额度、稳定性量化、利润、采购账单或供应商门户；
- 后台账号密码登录、MFA 自动化或通用低代码协议配置；
- TokenKey 倍率、售价、定价或计费修改。
