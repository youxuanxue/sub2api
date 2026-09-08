---
title: Universal Key — 一把钥匙通全平台/全模型/全模态
status: approved
approved_by: feng (对话审批 2026-06-19)
approved_at: 2026-06-19
authors: [agent]
created: 2026-06-19
related_prs: []
related_stories: []
---

# Universal Key（全能 Key）

本文保留 key、授权跨度与计费绑定契约。候选资格、当前容量、空池降权与调度策略由
[`candidate-eligibility-ssot.md`](candidate-eligibility-ssot.md) 统一规定；该契约已替代
旧版模型平台 hint 优先和支持/可用性混合判定。发现接口与站内菜单见
[`universal-key-capability-discovery.md`](universal-key-capability-discovery.md)。

## 实施状态

2026-09-08 对话已确认将“授权范围内、同付费层跨组统一选号”归入 candidate SSOT。
具体策略维护在该契约的 `Approved policy` 节。本分支已接入共享候选池、计费重绑、
会话身份和发现投影；验证记录见 US-050。本任务禁止发版部署，本地实现不等于线上生效。

新流程中，本契约继续拥有授权范围、订阅优先与余额回落、价格和扣费绑定；candidate
SSOT 消费这些约束。模型映射和协议合法性继续归协议路由 owner。

同日复审澄清：组的平台标签不再限制挂组账号的模型候选；授权范围内的换账号、换来源
属于正常候选重选。旧的“不跨组转发”不是新策略的约束。订阅读取、窗口维护或候选
评估失败时允许尝试已验证的余额路径，仍须通过钱包和 key 额度检查；错误须可诊断。
这不允许在授权范围未知时猜测授权。下方计费归属规则已接入候选选择及重试。

<a id="target-billing-attribution"></a>
### 已确认的余额计费归属

**2026-09-08 用户已确认**：保留现有价目表、用户倍率和扣费链，不新增全局
“用户 + 模型”定价体系，不通过批量改倍率或撤销挂组消除冲突。

在余额层，同一实际账号、同一请求和执行策略有多个合法授权来源，且适用价目表相同，
仅有效倍率不同时，使用最低有效倍率的来源结算。有效倍率由现有计费 owner 计算，
包含适用的用户覆盖和时段因素；不能仅比较数据库的组默认倍率。
来源必须独立满足授权、端点、请求策略、额度和适用的利润约束。

后续复审决定（2026-09-08）：倍率查询失败允许使用基础倍率 `1` 兜底，并记录错误；
兜底值不写入成功缓存，恢复后重新读取。查询成功但没有用户覆盖时仍使用组默认倍率。
共享查询不能因首个请求取消而连带失败，数据库读取保留独立的有限超时。
倍率兜底只解决价格取值，不替代授权、钱包、key 额度或订阅检查；既有时段与媒体
倍率组合规则仍由计费 owner 应用。最低倍率比较须遵循这一已批准的失败策略。

渠道查询失败必须与“查询成功但没有渠道”区分。当前分支已修复错误短缓存：
首次及后续缓存命中均保留查询错误，短期退避到期后重新查询，恢复后使用正常缓存。
适用价目表的比较必须消费可返回错误的配置读取（现有 `GetChannelForGroup` 已返回
渠道及其价目表）；读取失败不能判为同价目表。旧 `GetChannelModelPricing` 等便捷
接口保留其既有降级行为，不能用其空值或默认价格证明配置相同。倍率允许兜底 `1`
不等于价目表允许按空配置兜底。`candidate_billing_tk.go` 直接消费带错误的渠道快照。
实际模型价目表复用结算 owner `settleBillingOnAccountServedModel`；影子账号按结算链
解析母账号的计价映射，执行候选仍保留影子账号自己的 Plan。

这仅决定同一执行候选的计费来源，不在不同账号之间按价格排序。Direct 只在绑定组
授权范围内计费；Universal 只比较其有效授权范围内的来源。完全等价、同价来源可取
稳定的记账标签，但组 `sort_order/id` 不得决定收费高低或增加选号权重。

不同价目表（包括适用的模型、渠道或媒体价格）、不同请求策略、不同订阅权益，
不属于“同价目表、仅倍率不同”的比较范围。禁止只看倍率就宣布其中一条最便宜，
也禁止把本规则解释为“同模型无论换哪个上游账号都保证同价”。

2026-09-08 17:04–17:14 Asia/Shanghai 生产只读复核未发现同付费层内已配置的
非等价价目表冲突：各组模型价卡为空，唯一活跃渠道未配置模型价格。组 2/22 的
compaction 不同，但分别属于余额/订阅层，不能作为同层冲突的现网证据。
这不是未来配置的保证，也不是引入综合价格比较器的依据。

计费 owner 必须返回与所选执行路径一致的来源，供余额/订阅校验、价格、利润准入、
预留和最终记账共用。额度检查失败或重选时先清理旧预留，不能复用另一来源的校验结果。
“先选账号，再向一个不覆盖该路径的订阅扣费”不合法。既有订阅优先政策继续适用；
本节不新增多个不同订阅之间的消耗顺序。

管理员修改倍率后，预留估算与最终记账按现有缓存生效节奏读取到不同倍率是允许的。
本契约不要求冻结请求报价；共用来源不等于锁价，预留与实际扣费差额仍走既有结算链。

上线前需核对扩大授权模型范围后的折扣和成本影响。规则复用现有配置不等于收入不变，
也不能假设未启用的利润控制会自动兜底。

延期 note（用户已确认）：现有利润门仍有按计费组平台安装的限制。实际账号决定
执行 handler 后可能漏装原来源的利润门；本轮不改该机制，不作为此次设计收敛的
阻塞项，也不宣称新路径获得了额外利润保证。未来启用或扩展利润控制时需覆盖此边界。

用户后续确认 Direct 保留组级模型映射，Universal 不读取组级模型映射及其默认值；
账号映射和协议转换保留。具体契约、兼容边界与续接变更影响见
[`candidate-request-policy-convergence.md`](candidate-request-policy-convergence.md)。
组配置继续服务 Direct，不要求客户迁移客户端。映射模式隔离、统一选号与续接存储
迁移均在本分支实现，尚未部署。

### Platform quota boundary

2026-09-08 后续对话已确认：Universal 以用户、Key 的总体预算与实际计费来源额度为约束；
平台限额不能继续依赖计费组的平台标签。组退出调度排序，不代表钱包、Key 总额度或
订阅时窗额度退出准入。

这里的平台限额专指 `user_platform_quotas` 的“用户 × 平台”日/周/月 USD 上限，
不是组的订阅额度。现有 `BillingCacheService.CheckBillingEligibility` 仅在余额模式
检查它，订阅模式豁免。管理员用户管理页及注册默认设置保留该功能入口。

生产只读快照（2026-09-08 16:05 Asia/Shanghai）：16 位用户有 48 条未删除的记录，
25 条有用量累计，设置了任意日/周/月限额的记录为 0；全局与 7 种注册来源的默认值
也全部未设限。因此当前有计量，没有配置生效的限额，不能把归属缺口描述为已发生的
线上限额绕过。此数据是时点证据，不代表之后的新配置。

已知设计问题是相同执行可能随付款来源改变配额标签，例如账号 115 的同一 Claude
请求，经组 1 记入 anthropic，经组 19 记入 newapi。未来启用限额时可能造成绕过或误拒；
当前主要影响统计解释。本轮不新增模型平台分类体系、不改 Direct 的平台限额语义、
不删除线上配额数据。Universal 的准入与记账已退出计费组标签驱动的平台限额。
若未来需要“Claude 每日
预算”等能力，应先单独明确产品归类和计量口径，不能简单改用多厂商账号的平台字段。

### 请求读取与 WebSocket

HTTP 完整读取成功后才进入候选流程；超限返回 413，读取失败拒绝，不能拿部分内容
继续选择候选或预留费用。原始请求缓冲保留读取错误，正常预读不改变字节或编码头。
本分支已修复此读取路径；模型别名规范化是已有业务操作，不是缓冲读取的副作用。

WebSocket 与 HTTP 共用授权、候选与支付政策，但在首条 `response.create` 到达后才
具备完整请求。需先验证续接所有者/实际账号，再查其合法授权路径、执行订阅优先与
计费准入；握手仅验证连接身份不能代替首帧授权。后续每轮重新读取 Key、刷新授权范围，
重新检查支付条件和实际账号。真实 socket 回归覆盖首帧、后续轮次和零余额订阅。
具体验收边界只维护在 candidate 契约的 `Request ingress and transport` 节。

Grok 账号映射及默认/UI 不一致按用户要求另案解决，不改变本次已批准的账号映射边界。

## Key 行为

客户痛点:为用全平台,要管一堆按平台切分的 key/分组(anthropic、openai、gemini、grok、kiro、
newapi/扩展引擎……)。要的是 **一把 key,什么都能用**。

本设计让 **key 默认就是「全能」**:全能 key 不绑死平台,**每个请求**按请求的模型 + 入口端点,
在 **key 主人有权访问的所有分组**(公开组 + 专属授权 + 生效订阅)内选择合法账号和
计费路径。`apiKey.Group/GroupID` 在请求内表示当前计费来源；执行平台和协议来自
实际账号及 Plan。普通 Direct key 的范围仍限定为绑定组。

保留 **默认开启** 的开关；关闭后手选一个组，生成限定该组授权范围的 Direct key。
Direct 保留组级模型兼容映射；绑定组不等于按该组的平台标签限制实际账号。

不新增协议入口、全局别名配置或定价体系；复用既有账号能力、转发、并发和扣费 owner。

## 1. 计费(关键)

每个请求按所选合法来源的价目表和用户倍率记账。来源的平台名称不能推导模型类别或
执行路径。`candidate_billing_tk.go` 选择来源，`CandidateRequest.bind` 完成预留切换，
异步记账前固定 Key、来源、订阅和实际渠道映射快照。价格可随管理配置变化，来源不能错绑。

## 2. 让它正确的一条铁律

执行前必须确定 **实际账号、合法计费来源和订阅状态**。仅修改 `apiKey.Group/GroupID`
而不更新订阅和预留，会让执行与扣费分离。`CandidateRequest.bind` 与绑定观察器统一
更新请求内状态，handler 通过 `CandidateSubscription` 读取当前订阅。

HTTP 在 auth 内建立候选和计费路径，再进入正常支付校验。WebSocket 在首帧具备模型和
续接信息后完成同一流程。重试更换来源时同步更新请求内订阅，并先释放旧预留；释放失败
阻止重复预留，自身有效预留也不能被误认为钱包余额不足。

## 3. 设计

### 3.1 数据模型
- `api_keys.routing_mode` enum `{direct(默认), universal}`(`ent/schema/api_key.go` + 迁移
  `tk_034`)。**DB 默认 `direct`**,存量 key 不被翻动;**新建 key 默认 universal**(显式值优先;
  不带分组创建→universal;带分组创建→direct)。
- 字段经 `routing_mode` 贯通到 service 结构、repo 映射/读写、**auth 快照**、
  DTO/handler、Create/Update。读取模式本身不增加数据库查询；候选、授权和计费校验
  仍消费各自存储及缓存。

### 3.2 解析(per request)
- `universal_routing_tk_endpoint_map.go` 将 `c.FullPath()` 和方法映射为 `UniversalShape`。
  生产接线 `ProvideTKUniversalModelsProvider` 把 gateway、OpenAI、订阅服务和 router
  注入同一个 resolver。HTTP 认证调用 `PrepareCandidateIngress`，WebSocket 首帧调用
  `PrepareCandidateWebSocket`；二者建立同一个 `CandidateRequest`。
- resolver 提供有效授权跨度；`candidate_selection_tk.go` 批量读取这些组的实际账号，
  按完整账号/模型/端点路径消费 `evaluatePath`，不先选组、不按组平台求交。
  `candidate_request_tk.go` 在 Plan 前应用 Direct 兼容处理及渠道映射。受治理文本由
  `protocolrouter.Plan` 裁决，native/media 继续使用既有 capability owner。
  端点授权由实际账号和 Plan 判断；能力存在与上游 live servability 分开验证。

  共享评估区分“支持请求”和“现在可用”：有授权支持但容量暂不可用返回协议形状的 429；
  无有效授权返回 403，已有授权但不支持请求模型返回 400；能力证据未知或取数失败
  不能当成无权限。保留主线的 Gemini/Responses/count_tokens 合法 converter 与
  unsupported-model 错误分类，不恢复旧模型前缀门或 403-only 分类。
  订阅优先、同付费层去重、全局空池恢复和账号排序只维护在 candidate 契约中。

  **兼容路径**：未接候选调度服务或没有推理模型名时，`Resolve` 仍保留旧 provider
  分支，供兼容调用及测试使用。生产能力发现通过 `DiscoverCandidates` 复用
  `evaluatePath` 和计费策略等价性判断，不消费当前槽位、余额或订阅剩余额度。
  `universal_routing_tk_serving.go` 提供这些适配器和非治理路径的 capability helper。
  旧 provider 的 hint/服务集 fallback 不决定生产推理候选；候选错误不会触发旧式选组。

  **映射配置**：NewAPI 多 vendor 账号的非空 `model_mapping` 仍由账号写入校验
  (`ErrNewapiModelMappingRequired`) 和 `ops/newapi/audit-model-mapping.py` 审计维护。
  映射存在不等于具有合法协议路径或当前容量。
- `apiKey.Group/GroupID` 只表示当前合法计费来源。路由消费 `CandidateExecutionPlatform`，
  转发消费实际账号及所选 Plan；重试可切换到另一条完整合法路径，并重做预留绑定。
  已有 `ForcePlatform`（如 `/antigravity`）继续限制实际账号平台，不能从计费组标签派生。

### 3.3 热路径缓存
解析器通过进程内 per-user TTL 缓存与 singleflight 复用权限跨度；新授权受缓存过期和
`Invalidate(userID)` 影响。缓存只覆盖授权跨度，不保证每次候选评估零数据库读取。

## 4. 能力与边界

**能力**：一把 key 使用其授权范围内账号的合法模型及协议路径；按合法来源记账；
新授权按授权缓存生效。会话跟随实际账号，供应源凭据故障共享，模型冷却保留模型维度。

**边界**:
1. 全能不等于无限；范围是有效公开组、专属授权及订阅授权覆盖的完整候选路径。
2. 一次尝试只使用一个账号；可重试请求能在授权范围内更换账号或平台，重做计费绑定。
   已开始输出、硬续接和非幂等操作仍受既有重试安全约束，不能借重选重复执行。
3. 无合法路径时返回相应授权、模型、容量或配置错误，不猜测授权或静默替换客户模型。
4. 同名模型可有多个合法后端；候选排序由 candidate 契约裁决，模型平台 hint 不增加优先级。
5. `count_tokens` 按请求与实际账号能力收敛，保留主线新增的合法模型和协议路径；
   `/v1/models` 等发现入口使用 discovery 契约的协议投影，不永久绑定单组。
6. 全能 key 的授权面覆盖该用户全部有效授权，宜配 key 级总额度；需要限定单组时使用 Direct。
7. images/edits 缓冲完整的受大小限制的原始请求体，再提取 JSON/multipart 的 `model`
   字段；multipart parser 跳过其他 part，不等于上传内容没有被读入内存。重复预读复用
   成功缓冲，读取失败终止。video submit 读取 JSON 模型名，poll 复用任务记录里的
   submit-time 路由。
8. 后台没有的能力变不出来(无视频账号的平台不会凭空有视频)。

## 5. 守卫

`scripts/sentinels/gateway-tk.json` 同时锚定候选 owner、生产接线、认证/执行消费点及测试。
端点与协议能力继续消费 engine/protocolrouter 的事实；旧组级兼容函数不能替代生产调用锚点。

## 6. 验证

候选验收与可执行命令见
[`US-050`](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md)。
key 交互与能力发现的验收由 discovery 契约维护；历史 PR 阶段拆分见 git 历史。
