# TokenKey 模型供应源管理设计

## 1. 决策摘要

供应源是低频管理面工具：运营保存供应事实，确认后将其编译成 TokenKey 现有账号配置。

```text
model_supplier_sources
        ↓ 保存、校验并同步
供应源同步服务
        ↓ 只提交账号配置命令
现有账号领域服务 ─────────→ accounts.priority ─────────→ 现有 scheduler
```

账号组管理是独立既有能力，不进入这条供应源链路。

最终决策：

1. 唯一管理对象是“供应源”；
2. 所有新供应源 `base_priority=100`，运营查看全局预览后自行修改；
3. 采购比例只归入六个固定档位，账号仍只按现有 `priority` 调度；
4. 保存不碰账号；“校验并同步”真实探测后按先增后减写账号；
5. 供应源完全不感知账号组，不修改 scheduler；
6. `supplier_source_id` 标记受管账号，通用账号写入口由 service 层统一拒绝；
7. 账号列表和详情显眼显示“供应源托管”。

运营只有一条主流程：

```text
保存供应事实 → 查看全局 priority → 校验并同步 → 查看本次探测与账号变更结果
```

## 2. 边界

### 2.1 R-002：供应源与账号组完全隔离

账号组决定账号进入哪个既有路由域，属于账号与网关领域，不属于采购事实。供应源因此：

- 不依赖 `GroupRepository`；
- 不读取、比较或写入 `account_groups`、`group_ids`；
- 供应投影读取只调用 `GetSupplierAccount`、`ListSupplierManagedAccounts`、
  `ListSupplierAdoptionCandidates` 等专用窄查询，不调用会加载账号组关系的通用 `GetByID`；
- 专用读取返回的账号对象必须清空 `AccountGroups`、`GroupIDs`、`Groups`，不能把账号组带回供应源服务；
- 不计算目标模型对应账号组，不取账号组并集；
- 不把账号组作为已有账号匹配或复用条件；
- preview、同步响应和供应源页面不展示账号组差异；
- 不调用 `BindGroups`，账号组变化也不触发供应源同步。

新档位账号必须调用现有账号创建服务，不直接写账号仓储。未显式传 `group_ids` 时，仅借用现有
NewAPI 默认组规则：存在 `newapi-default` 就尝试绑定，不存在则保持未分组。默认组策略校验或绑定失败
时，供应账号保持未分组，供应源同步继续；普通 `CreateAccount` 的既有失败契约不变，不能因供应源的
best-effort 需求而变成“返回已创建账号和错误”。默认组结果由账号领域独立拥有，不影响供应源同步
成败。

已有账号被接管时保留原账号组；以后仍由账号组管理服务绑定或解绑，供应源同步不得覆盖。“供应源
同步成功”只表示账号配置已收敛，不表示账号进入了任何特定账号组。

### 2.2 非目标

第一版不：

- 修改 gateway、scheduler、sticky、运行期健康窗口、冷却、限流或 fallback；账号
  `status/schedulable` 只按目标 mapping 收敛，不引入新的健康算法；
- 引入 `native → supplier → unmanaged` 调度层、`tk_source_class` 或原生 OAuth 回填；
- 读取或写入倍率、售价、定价、pricing registry 或用户计费；
- 引入供应源状态机、启用、暂停、紧急止损、删除或归档；
- 引入独立供应模型表、探测历史、同步任务或供应源专用审计；
- 引入蓝绿账号、影子 revision、跨账号事务或全量回滚；
- 自动猜测协议或模型 ID，建设通用低代码协议配置；
- 建设供应商、合同、容量、扩容能力或稳定性评分等额外实体；
- 把供应源字段暴露到普通用户 API。

## 3. 管理对象与数据契约

一个供应源代表：

```text
一个供应商通道 + 一个 endpoint + 一份 API Key + 一个 base_priority + 一组明确采购模型
```

供应商、endpoint、并行使用的 API Key、channel 或合同通道任一不同，就建立不同供应源。不同供应商
必然使用不同供应源和不同受管账号。

日常 API Key 轮换在原供应源更新，保留 `source_id`；只有两份凭证需要并行供给时才拆源。系统拒绝
重复创建“同一供应商、同一通道、同一规范化 endpoint、同一凭证”的供应源。

### 3.1 单表

第一版只新增 `model_supplier_sources`：

| 字段 | 含义 |
| --- | --- |
| `id` | 供应源 ID |
| `supplier_name` | 供应商名称 |
| `channel_name` | 供应通道或合同模型组 |
| `endpoint` | 上游基地址 |
| `encrypted_credential` | 加密 API Key |
| `credential_fingerprint` | 内部 HMAC 派生值 |
| `base_priority` | 默认 100；必须保证 `base_priority + 6` 落入 PostgreSQL `INTEGER` |
| `models` | JSONB 模型采购事实 |
| `notes` | 开放备注，不参与运行判断 |
| `created_at` / `updated_at` | 时间戳 |

`credential_fingerprint` 只用于查重和已有账号窄匹配：不通过 API 返回，不写账号 Extra，不进入日志
或 scheduler。HMAC 使用现有凭证加密密钥的生命周期：JWT secret 轮换不改变指纹，凭证加密密钥轮换
会改变指纹。供应源不保存后台账号密码，也不自动处理登录、MFA、验证码或创建 API Key。

不增加 `state`、`enabled`、`revision`、`applied_revision`、探测状态或投影账号 ID。

### 3.2 模型采购事实

```json
[
  {
    "client_model_id": "deepseek-v4-pro",
    "upstream_model_id": "deepseek-v4-pro",
    "purchase_ratio": 0.5
  }
]
```

| 字段 | 约束 |
| --- | --- |
| `client_model_id` | TokenKey 客户模型 ID；同一供应源内唯一 |
| `upstream_model_id` | 上游实际接受的模型 ID；必须逐行明确 |
| `purchase_ratio` | 可空；非空时必须满足 `0 < ratio <= 1` |

禁止普通 `*`、“全系列”和 `43折`、`55折` 等模糊文本。允许空模型清单，表示当前没有目标供给。

### 3.3 凭证保护

- API 不回显明文、密文或 HMAC 指纹；
- 编辑时凭证留空表示沿用，填写新值才轮换；
- 轮换前检查不会与其他供应源形成重复身份；
- 日志和 Admin Audit Log 不记录凭证明文或上游原始响应；
- 同步时把凭证写入账号现有 credentials；gateway 仍只读账号凭证。

## 4. 折扣档、priority 与账号投影

采购比例只用于选择固定档位并间接生成账号 `priority`：

| 档位 | purchase ratio | discount priority |
| --- | --- | --- |
| 1 | `0 < ratio < 0.20` | 1 |
| 2 | `0.20 <= ratio < 0.40` | 2 |
| 3 | `0.40 <= ratio < 0.60` | 3 |
| 4 | `0.60 <= ratio < 0.80` | 4 |
| 5 | `0.80 <= ratio < 1.00` | 5 |
| 6 | `ratio = 1.00` 或为空 | 6 |

```text
account.priority = source.base_priority + discount_priority
```

所有新供应源从 100 开始；系统不按供应商、价格、错误率或账号类型自动调整。priority 重叠、区间
交叉或供应账号排在其他账号之前只提示，由运营判断。原生 OAuth priority 不由供应源读取或修改；
相同 priority 后的选择继续使用 TokenKey 现有规则。

同一供应源、同一折扣档的模型合并到一个受管账号，每个供应源最多六个账号。受管账号 Extra 只
增加：

```text
supplier_source_id
supplier_discount_band
```

`supplier_source_id + supplier_discount_band` 是唯一逻辑身份；账号组不参与判断。没有匹配就创建，
唯一匹配就复用，匹配到多个就停止并返回冲突。

档位最后一个模型被移走后，保留账号和账号 ID，清空 `model_mapping` 并设
`schedulable=false`，以后该档再次出现模型时复用。不自动删除、归档或复制受管账号。

## 5. 保存、预览与同步

### 5.1 保存与全局预览

“保存”只校验、规范化并更新供应源单表，不探测上游，不创建或修改账号，也不落“待同步”等技术
状态。是否需要同步由运营根据刚保存的事实和最近一次同步结果判断。

已选来源的表单只用当前表单与已保存快照派生是否有未保存修改；有修改时禁用“校验并同步”并提示
先保存。这里不新增 dirty 状态、revision 或后端草稿，sync API 始终读取数据库中已保存的供应事实。

全局预览只计算全部供应源目标档位的 priority，按数值排序并提示供应源之间的相同 priority；它不扫
描普通账号、OAuth 账号或账号组，也不替运营推断完整线上调度顺序。运营结合账号管理页的实际
priority 自行判断。页面只提供单源同步，不提供全部同步。

### 5.2 目标配置与探测

目标档位账号只包含：账号名称、固定 `platform=newapi`、`type=apikey`、OpenAI Chat `channel_type`、
endpoint、API Key、完整 `model_mapping`、最终 priority 和两个受管 Extra；不包含账号组，也不以
“模型是否已配置正式组”作为同步前置条件。结构同步会把受管账号的 transport 漂移修复回这些固定值。

变更分类：

- 仅备注变化，或比例变化后仍在原档：账号配置不变，不探测；
- 供应商名称、通道名称或 `base_priority` 变化：只更新账号名称和 priority，不探测；
- endpoint、credential、模型 ID、模型增减或跨档：结构变化；
- 受管账号 `status/schedulable` 与目标投影不一致：投影漂移。mapping 非空时先探测再恢复为
  `active + schedulable`；mapping 为空时无需探测，直接收敛为 `active + schedulable=false`。

结构变化使用带受管身份的内存目标账号、目标 endpoint、凭证、固定 transport 和明确 upstream model ID
逐模型真实探测。供应源 OpenAI Chat endpoint 允许保存到 `/v1`，但只在受管账号和预探测账号进入
NewAPI OpenAI 适配器前去掉一个末尾 `/v1`，由适配器统一拼成 `/v1/chat/completions`；普通 NewAPI
账号的 base URL 行为不变。

探测门禁：

- 任一失败：返回全部当次结果，不写账号；
- 全部成功：生成只在本次同步调用链内使用的 Chat 正向探测证据，并立即同步账号；
- 空模型清单：无需探测，直接收敛为空投影。

本次探测结果返回客户模型 ID、上游模型 ID、成功/失败、协议分类和固定脱敏说明；不记录或返回上游
原始响应。档位与目标
priority 已由供应源表单和全局预览直接派生，不在探测结果中重复。结果只存在于当次响应和页面，
不建设供应源探测历史。内部 Chat 正向证据不是运营状态，也不落供应源表；它只用于授权紧随其后的
非空账号投影，并发布到现有协议能力契约。

### 5.3 先增后减

页面在请求期间禁用重复提交。同一供应源不支持人工并发编辑或并发同步；服务每次都从当前供应事实
与真实账号重新计算差异，不为这一低频管理面引入分布式锁、任务状态或发布编排。

全部目标模型探测成功后，供应源同步服务通过现有账号领域服务执行：

1. 创建缺少的档位账号，初始 `model_mapping={}`、`schedulable=false`；账号创建服务 best-effort 尝试
   默认组，失败时保持未分组并继续；
2. 非空 mapping 只能通过携带本次 Chat 正向探测证据的窄账号命令写入；service 和 repository 都拒绝
   未经探测的非空投影；
3. repository 在单个数据库事务内写固定 transport、endpoint、credential、目标 mapping、最终
   priority、`status/schedulable` 和受管标记，同时链接并发布仅含 Chat Completions 的现有协议能力，
   写入 `InitialProbeCompleted=true`、`OfficialSeed=false` 的正向证据，并提交普通 scheduler outbox；
4. 事务提交后只读回账号配置确认目标 mapping 已存在，不再发起写后二次网络探测；
5. 再从旧档位账号删除不再属于该档的 mapping；非空剩余 mapping 复用同一次全量预探测证据；
6. mapping 为空的账号无需探测证据，设 `schedulable=false`；
7. 返回实际完成的账号、mapping 和 priority 变化。

跨档示例：

```text
先给目标档账号增加模型并确认可调度 → 再从原档账号删除模型
```

endpoint 或 credential 变化时，探测成功后原地更新。增加阶段使用“仍属于目标清单的当前 mapping”
与“本档目标 mapping”的并集，减少阶段再收敛为本档精确 mapping；不把已删除模型带到新配置。

不暂停供应源，不建设蓝绿账号或调度屏障。跨账号写入允许短暂重复供给，不承诺跨账号原子性；每个
账号的配置、Chat-only 协议能力和 scheduler outbox 必须原子提交。

### 5.4 失败与重试

任一同步错误都返回当前 `failed_step + changes[]`。单账号原子投影失败时，已有账号保持旧配置；刚
创建的账号保持空 mapping、不可调度。跨账号流程立即停止，保留此前已成功提交的增加，不执行尚未
开始的减少，不做跨账号回滚。重试重新计算目标与实际差异，幂等继续收敛。

## 6. 已有账号复用与协议边界

第一版仅在以下条件全部满足时自动复用普通已有账号：

1. 规范化 endpoint 和 API Key HMAC 指纹完全一致；
2. platform/transport 是 NewAPI OpenAI-compatible Chat；
3. 只匹配到一个账号，且尚未受其他供应源管理；
4. 供应源只有一个非空折扣档；
5. 已有账号所有 mapping 都在目标 mapping 中完全相同；允许新增，不接管含目标外 mapping 的账号。
6. 已有账号当前 `status=active`。

账号组不参与匹配，复用前后保持不变。匹配只在同步时执行；保存不查找账号。候选查询覆盖全部未删除
NewAPI 账号，以便 `disabled/error` 的精确 endpoint + 凭证匹配也能阻止重复建号；这类非 active 账号
返回冲突，由运营先在普通账号入口判断是否恢复。复用后写入两个受管 Extra，并由供应源拥有第 7 节
定义的配置。任一条件不满足就停止并返回冲突，不自动选择、合并、拆分或覆盖。

第一版新建账号固定使用 NewAPI OpenAI-compatible Chat API Key transport；运营不选择 platform、
channel type 或协议。即使探测事件证明 Responses 或 Anthropic Messages 成功，这类非 Chat transport
仍返回 `protocol_unsupported`；Embedding、图片、视频及私有异步协议同样失败封闭，不写账号。

受管账号只声明 Chat Completions 协议 endpoint 和 capability，不参与通用 Messages/Responses 探测。
这个限制由 `supplier_source_id` 识别；普通 NewAPI 账号的 endpoint、协议声明和 base URL 行为不因
供应源功能改变。

FMGo 首批只保存显式 Seedance 双 ID：

```text
client_model_id   = doubao-seedance-2-0-260128
upstream_model_id = feimiao-seedance-2-0-260128
```

这不是通用前缀替换规则；视频适配器完成前，FMGo 只能得到 `protocol_unsupported`。

## 7. 账号所有权、service 保护与 UI 标识

存在 `supplier_source_id` 的账号是供应源受管账号。字段所有权只有三类：

- 供应源拥有：账号名称和固定 transport、endpoint、API Key、`model_mapping`、priority、`status`、
  `schedulable` 以及两个受管 Extra；
- 账号组服务拥有：账号组绑定；
- 运行期服务拥有：健康、错误、冷却、限流、最后使用时间等运行字段。

UI 禁用不是安全边界。单账号更新、批量更新、credentials/Extra 更新、credentials 刷新、
status/schedulable/priority 切换、复制、删除、导入覆盖等所有通用账号配置写入口，必须在 service 层
调用同一个保护规则：只要存在 `supplier_source_id` 就整体拒绝，不做字段级猜测或部分放行。

普通创建和导入同时拒绝外部提交保留字段 `supplier_source_id`、`supplier_discount_band`。唯一可写
供应源拥有字段的是供应源同步调用的专用内部账号命令。恢复错误、配额重置、代理 fallback 和账号
探测等运行期专用操作继续允许；账号组和运行期专用 service 只能写各自拥有的数据。

账号列表和详情都显示高辨识度徽标：

```text
供应源托管 · {supplier_name}/{channel_name}
```

Admin 账号 DTO 仍只通过 Extra 暴露 `supplier_source_id`。前端用供应源列表映射名称；徽标链接携带
`source_id`，供应源页面加载列表后直接选中目标来源，未找到时保持普通列表。名称加载失败时回退为
`供应源托管 #<source_id>`，账号查询 service 不反向依赖供应源仓储；账号页重新挂载时轻量刷新目录，
避免同一会话内供应源改名后仍显示旧名称。

普通编辑、批量编辑、切换状态、复制和删除入口显示明确原因，不能只静默置灰：

```text
该账号由供应源托管，请前往供应源管理修改。
```

账号组继续在账号组管理入口独立维护，不放入供应源页面。

## 8. API 与页面

第一版 API：

```text
GET    /admin/supplier-sources
GET    /admin/supplier-sources/:id
POST   /admin/supplier-sources
PUT    /admin/supplier-sources/:id
GET    /admin/supplier-sources/priority-preview
POST   /admin/supplier-sources/:id/sync
```

不提供 DELETE、validate、activation-preview、activate、pause 或供应源 audits API。供应源 API、preview
和 sync 响应不包含 `group_ids` 或账号组 diff。

页面只提供运营字段、全局 priority、保存、单源校验并同步，以及同步后返回的当次探测与实际账号
变化。不展示预先扫描的全账号 diff、账号组、倍率、售价、计费、协议配置、capacity、并发、技术
状态机或探测历史。

## 9. 首批验收案例

### 9.1 佳杰 / VSTECS

首批只录入同模型系列最低合法采购比例：

```text
deepseek-v4-pro  ratio 0.50 → 档位 3
qwen-3.7-max     ratio 0.50 → 档位 3
```

两者合并到一个档位 3 账号；逐模型真实探测全部成功后才写账号。没有真实 API Key 时只能保存，不能
完成同步验收。

### 9.2 FMGo

只录入 Seedance 显式双 ID，ratio 0.50。第一版必须返回 `protocol_unsupported` 且不写账号。

### 9.3 百度千帆

账号 90 的只读库存显示 `channel_type=46`，不符合第一版固定的 OpenAI Chat transport，因此首批不
接管；缺少供应凭证也不能完成指纹匹配。它只保留为线上差异证据。

## 10. 上线影响与旧实现删除

当前实现和 migration 尚未上线，因此直接重写，不兼容被否决的旧设计。

生产 migration 只新增供应源单表和必要索引：不修改现有账号或账号组，不回填 Extra，不重排
priority，不触发调度；旧镜像可以忽略该表。保存供应源同样不影响线上。

只有运营主动同步时，才通过账号领域服务修改：

```text
单个供应源 × 最多六个档位账号 × 明确声明的模型
```

新账号只 best-effort 借用人工创建普通 NewAPI 账号的默认组规则，策略校验或绑定失败时保持未分组并
继续供应源同步；普通账号创建失败契约不变。已有账号组不受同步影响。scheduler 仍只读原有
priority；普通账号的 gateway 和缓存语义保持不变。受管账号仅通过现有 Chat 路由和现有协议能力表
接入，不增加供应源专用调度或网关分支。

实现时删除旧草案留下的：

- scheduler 供应来源分层、`SupplierRoutingLayer`、`tk_source_class`；
- 原生账号 manifest、回填脚本及 OAuth import/edge relay 的 native 标记；
- 独立供应模型/审计表，供应源状态、revision、探测历史和 activation/pause 服务；
- activation preview、专用 audits、状态操作 API；
- 逐模型账号、全池 priority 重排、跨账号快照回滚；
- 供应源路径中的 `GroupRepository`、`ResolveSupplierGroupIDs`、`BindGroups`；
- preview/API/UI 的 `group_ids_before`、`group_ids_after` 和其他账号组 diff；
- 供应源直接调用账号 repository 创建或更新账号的旁路。

## 11. 验收标准

- 部署和保存供应源不会改变 scheduler、现有账号、priority 或账号组；普通 NewAPI 账号的 gateway
  base URL 和协议行为保持不变。
- 新供应源 base priority 默认 100；六档边界正确，`1.00` 和空比例进入档位 6。
- `base_priority + 6` 始终落入 PostgreSQL `INTEGER`，越界输入在保存前拒绝。
- 同源同档模型合并；每源最多六个账号；最终 priority 严格等于两项之和。
- 供应源不依赖账号组仓储，不读写账号组，专用投影读取不调用通用 `GetByID` 且不返回任何账号组
  关系；API/preview/sync 无账号组字段。
- 新账号经现有账号服务以空 mapping、不可调度状态创建；默认组失败时保持未分组并继续，普通账号
  创建契约不变；已有账号组在接管和同步后不变。
- priority-only 不探测；结构探测任一失败不写账号并返回本次逐模型结果。
- 已选来源有未保存表单修改时不能同步；非空受管账号的 `status/schedulable` 漂移先探测再收敛，空
  mapping 漂移无需探测并收敛为 `active + schedulable=false`。
- 固定 transport 为 NewAPI OpenAI Chat，结构同步会修复漂移；非 Chat 成功证据仍失败封闭。
- 受管/预探测账号只声明 Chat Completions；末尾 `/v1` 只在该受管路径规范化，普通 NewAPI 账号不受影响。
- 非空账号投影必须携带本次 Chat 正向探测证据；账号配置、Chat-only capability 和 scheduler outbox
  在单账号事务内提交，失败不留下半写配置，也不再执行写后二次通用探测。
- mapping 先增后减，非空才可调度；所有错误返回 `failed_step + changes[]`，中途失败保留增加、停止
  减少，重试可幂等收敛。
- 空档账号保留且不可调度；已有账号只按第 6 节窄条件复用，非 active 精确匹配只报冲突、不新建重复
  账号。
- 未支持协议失败封闭；佳杰、FMGo、百度案例留下真实请求或线上账号证据。
- 通用账号配置写入口（含 credentials 刷新）在 service 层拒绝受管账号；普通创建/导入不能伪造受管
  Extra；恢复错误、配额重置、代理 fallback 和探测仍可执行。
- 账号组和运行期 service 不能修改供应源拥有字段。
- 账号列表和详情显眼显示来源、跳转入口和明确只读原因。
- 表、API、日志和 Admin Audit Log 不泄露凭证、指纹或上游原始响应；凭证指纹跟随加密密钥而非 JWT
  secret 的生命周期。
- 供应源不读取或写入倍率、价格、定价和计费数据。
