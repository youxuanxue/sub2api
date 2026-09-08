---
title: Candidate request policy convergence and continuation migration
status: approved
approved_by: "feng (conversation approval, 2026-09-08)"
created: 2026-09-08
---

# Candidate 请求策略收敛与续接迁移

## 审批边界

用户在 2026-09-08 复审后确认：不能要求客户迁移客户端；Direct 保留原组级映射，
Universal 不再读取组级映射，并指示继续。本文记录该审批，不是生产部署证据。
已批准的调度、倍率兜底、正常
改价与端点权限方向仍由 [candidate SSOT](candidate-eligibility-ssot.md) 和
[Universal 计费契约](universal-key-routing.md) 维护。

原先“迁移客户端后全面删除组级映射”的方案已被替代。组配置继续服务 Direct，
不复制到账号、不批量修改 key 模式，也不删除现网持久化状态。
当前分支已实现映射模式隔离、授权范围内统一选号和硬续接状态兼容；本任务没有部署。
测试覆盖和尚缺的组合验收见 US-050，不能将本地实现等同于线上迁移完成。

## 模型映射实际上在做什么

| 职责 | 当前 owner | 收敛建议 |
| --- | --- | --- |
| Messages、Chat、Responses 之间的格式转换 | `protocolrouter.Plan` 与 converter | 保留；使用 Messages 不要求把模型改成 Claude |
| 公共模型规范名和历史名称归一化 | 现有模型 registry 与归一化 owner | 保留；不能把不同供应商模型当作同一模型的拼写别名 |
| 实际账号接受的公开模型及其上游名称 | `Account.GetMappedModel`，经 `account_supported_protocols.go` 输入 Plan | 作为账号映射唯一事实；候选和执行共同消费 |
| Claude 家族名替换为 GPT、GLM、Kimi 等 | 组 `MessagesDispatchModelConfig`、dispatch defaults registry 和 handler 改写 | Direct 保留；Universal 跳过 |

组级替换不只是 Claude Messages 转换：`openai_chat_completions.go` 和
`openai_gateway_handler_tk_responses_dispatch.go` 也消费它。
请求 `claude-opus-4-6` 经组 11、19、285 可分别成为 `deepseek-v4-pro`、`glm-5.2`、
`kimi-k3`。账号 88/116/117/127/128 同时覆盖这些模型和授权来源，无法把这些
互相冲突的替换自动合成同一账号的一条别名。

## 已批准的映射规则

请求明确真实模型时，任何合法协议入口都以该模型构建请求事实；例如
`/v1/messages` + `gpt-5.6-sol` 仍可交给合法 converter，不需要 Claude 名字过桥。
Direct 继续读取绑定组原有精确映射、家族映射和默认映射，保留现有适用条件和优先级。
Universal 不读取这些配置，也不按最终计费组的平台或名称选择默认映射；选择失败、
count_tokens 和转发重试不能重新引入组级替换。两种模式都保留账号映射和协议转换。

同样请求 `claude-opus-4-6`，Direct 绑定组 19 可继续按配置解释为 `glm-5.2`；
Universal 只寻找账号映射与 Plan 明确支持该请求的路径，无合法候选则返回相应错误。
不能为保可用而重新读取某个计费组的映射，也不自动向账号添加兼容别名。

账号的显式映射决定上游名称，Plan 决定实际协议与能力。对同一个有效请求模型，
同一个账号只有一个确定映射；Universal 的计费来源和挂组数量不能改写它。

Direct 的组映射表达该组的产品兼容策略，账号映射表达上游适配事实，两者不是同一
份别名配置。Universal 若有显式账号别名则继续由账号 owner 解释；不能把多个
组的冲突目标复制进账号，也不能自动挑最便宜的目标来解释客户端意图。
不同账号是否支持同一请求仍由各自明确映射与 Plan 裁决，合法 converter 不增加惩罚。

不新增另一套 Universal 全局别名表，不要求客户更新客户端或环境变量。
Direct 与 Universal 共用候选 owner 的前提是有效模型、请求策略和授权范围相同，
不是原始模型字符串相同。`CandidateRequest.pathContext` 在 Plan 前确定 Direct 的
有效请求，生产 handoff 与重试消费这一请求和所选 Plan，不在发送前再次应用组映射。

渠道配置可能同时参与模型重写和计费模型选择。迁移要把执行重写接入同一个
请求/账号映射流程，保留渠道价格和账单归因；不能直接删除 `channel.model_mapping`
或把不同价目表误认为账号别名。最终 Plan 产生后，handler 不得再改写其模型输入。

组级 compaction 等非模型策略由 `candidate_billing_tk.go` 按适用请求和实际账号
比较；同账号、同付费层的非等价来源返回 `ErrCandidatePolicyConflict`，不以倍率
强行决定执行策略，也不增加候选票数。其他合法账号或付费层仍可接单。
组 2/22 分别属于余额/订阅层，不是已查实的同层冲突；真实续接请求跨这两个来源时
的 compaction 影响仍属于发布前样本对照范围。

## 兼容边界与负面影响

按 key 的 `RoutingMode` 区分映射语义，不新增开关。认证给 Universal 绑定计费组后
仍保留 Universal 身份，不能仅凭 `GroupID != nil` 判为 Direct。跳过映射通过
消费入口守卫实现，不能清空组对象中的配置，否则会污染计费或共享缓存中的 Direct。
历史空 routing mode 沿用 Direct 行为。组字段、管理表单和默认值继续维护。

| 触发条件 | 直接删除的负面影响 | 切换前措施 |
| --- | --- | --- |
| Direct 客户端依赖 Claude 到其他供应商的组级替换 | 全面删除将破坏现有调用 | 保留原组级映射，无需客户迁移 |
| 存量 Universal 依赖计费组把 Claude 替换成其他模型 | 跳过后可能不支持，或选中真正 Claude，质量和费用改变 | 上线前按 key 对照旧/新路径；不宣称存量 Universal 完全无影响，不自动改成 Direct 缩小授权 |
| 一个账号经不同组把同一名字映射成不同模型 | 自动合并丢失客户端原意 | Direct 各自保留；Universal 不消费这些冲突组映射 |
| 渠道映射或 handler 在 Plan 后继续改 body | 候选承诺和真实执行不一致，可能上游 400 或错账 | 单一路径生成有效请求，重新解析 canonical request；发送消费同一 Plan |
| 自动选档和子任务模型不同于主模型 | 主请求成功不能证明其他调用兼容 | 对照分析覆盖子任务及压缩工作流，无客户端迁移前提 |

生产只读证据：2026-09-08 06:20 UTC（14:20 北京时间）的最近 24 小时查询，
上述调度组没有匹配到 `requested_model/model LIKE 'claude%'` 的计量行；
账号 63/64/68 仍有 Messages、Chat 和 Responses 流量。样本账号的 Claude 映射键
仅见账号 115 的 `claude-fable-5` 同名映射。数据来自 `usage_logs`、账号映射和组配置。
这不能证明所有存量 Universal 都不依赖旧替换：失败请求、
字段缺失、低频流量及样本外账号不由这次聚合覆盖。生产镜像为 1.8.205，
代码分析基准为 `origin/main@9ef0fbd818e9`，不可把 main 的行为当成已部署行为。

发布前需用真实请求样本对旧/新解析路径做离线对照，覆盖 Messages、Chat、Responses
及直接/全能 key；对受影响的存量 Universal 必须得出明确结果。保留可回滚版本和旧配置，新的
持久化状态要兼容回滚读取。不得用“先上线看报错”作为映射迁移的验收。

## 硬续接顺序及影响

已实现顺序：认证用户和 key → 查找并验证续接所有者/实际账号 → 按当前授权和请求
策略找可续接路径 → 在这些路径内订阅优先、再余额 → 准入和执行。
普通软粘滞只表达偏好，不进入硬账号限制。已提交媒体任务沿用 submit-time 路由，
不能因为轮询碰到其他可用订阅就重调度或重新提交。

| 场景 | 对服务的影响 | 必须保留的行为 |
| --- | --- | --- |
| 原账号忙，但另一个账号空闲 | 硬续接的可选容量更小，可能等待或返回容量错误 | 有限等待；普通新请求仍可换号，不能用换账号破坏续接 |
| 只有其他账号具备可用订阅 | 本次续接可能使用余额 | 原账号当前仍被授权且通过钱包/key 检查；不能向不覆盖该执行的订阅扣费 |
| 原账号被撤销、失效或已不能续接 | 有效历史所有者也可能无法继续 | 所有者记录不授予当前调用权；不默默开启新对话，不自动重放非幂等操作 |
| 归属缓存故障或缺失 | 增加读取依赖；不能把未知当成“新请求”继续转发 | 有限读取预算、可诊断错误；禁止绕过所有者验证 |
| 同一用户更换 key | 过窄的新命名空间会误拒绝现有合法续接 | 保留当前同用户不同 key 的互通，同时重新检查新 key 的授权和额度 |
| 滚动发布或回滚遇到旧 group-scoped 记录 | 查询不到已存在的 Responses/account 状态 | 旧硬状态可读，切换期新写入兼容回滚；普通软粘滞仍可一次性冷启动 |

线上账号 63/64/68 同属组 2/22，涉及 GPT/Codex；这次采样确认有 Responses 流量，
但计量行无法证明其中多少携带 `previous_response_id`。不能据此估计硬续接比例。
此前生产订阅快照没有生效订阅，支付层冲突尚无现网发生证据，需用测试验证。

`candidate_identity_tk.go` 以用户和 response ID 查询实际账号，计费组退出新索引身份。
`openai_ws_state_store.go` 同时写旧组命名空间及新用户索引；旧记录只在当前已授权
来源范围内兼容读取，不扫描 Redis 全库。所有者与账号必须来自同一命名空间。
状态测试使用区分命名空间的 cache double 和实际 Redis key 格式，覆盖跨用户拒绝、
同用户跨 key、旧记录读取、独立 store 重载、过期及存储异常。真实滚动发布和回滚
尚未执行；这些兼容测试不等于已完成生产演练。

## 实施与验证边界

映射隔离复用 `APIKey.IsUniversal()`：文本、Responses 重写、count_tokens
和选号回退通过 `resolveOpenAIMessagesDispatchMappedModelForContext`；图片默认模型
及其回退通过 `resolveOpenAIForwardDefaultMappedModel`；Gemini 桥接通过
`tkGroupFromGinContext`。认证与 resolver 通过 `WithUniversalKeyRouting` 保留请求身份，
Gemini 模型冷却判断复用 `resolveGeminiForwardModels`，避免按计费组的旧目标误拒绝，
或漏掉实际账号模型的限流。这些模式守卫不改写持久化账号映射或组配置；合法授权
和计费来源由候选 owner 决定。
验收和测试入口维护于 [US-050](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md)。

统一调度、实际 Plan 端点授权、计费重绑和硬续接兼容均已接入生产调用链。
US-050 记录当前测试边界；发布前的真实请求对照、滚动切换及回滚验收仍须完成。
用户禁止本任务发版部署，线上配置变更和部署不属于本次完成声明。
