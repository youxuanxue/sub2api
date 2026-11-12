---
title: 网关错误、调度与安全边界修复
status: approved
approved_by: user (conversation confirmation 2026-11-12)
created: 2026-11-12
authors: [codex]
risk: high
---

# 网关错误、调度与安全边界修复

## Scope

承接用户要求“请先核实并修复 2，3，4，5”，用户于 2026-11-12 回复“确认。”批准实施和验证。本文件是具体设计的审批基线，
不是生产变更授权，也不表示下列代码已经修复或上线。
安全边界、协议错误语义及数据库并发变更按高风险路径推进。

只处理上轮核验中：NewAPI 长请求最终 502、Chat SSE 流内错误、scheduler outbox、
group usage rollup、Codex 会话/错误摘要、refresh token 轮转、默认日志脱敏。
prompt audit 原文出口列为本轮安全子任务，但不改独立 QA raw 归档契约。

不做：EasyPay、客户端版本升级、模型上下架、供应源重分配、提高超时/重试次数、
生产配置修改、历史日志改写、历史数据清理、合并或部署。保持 Plan/candidate、
availability、计费身份和 QA 生命周期的既有 SSOT。

## Verified Baseline

实施前再次 fetch 并快进至 `origin/main 016bed1fe`，已包含 PR #3161 的取消归类修复。
交付前于同日再次快进至 `origin/main 6fd62e8fa`，保留其 Gemini 定价与 Kimi 兼容修复。
下列只读生产证据采集时的基线为 `26af4721d`。

2026-11-12 01:56 UTC（北京时间 09:56）只读复核：

- prod 自 00:30 UTC 起 Fable 5.1 `/v1/messages` 最终 502 有 23 条，最近一条在
  01:08:56 UTC。不能仅凭这些统计把错误归为供应商故障。
- 对一条失败做请求 ID 关联：上游调用返回 `context canceled`，随后
  `fallback_error_response_written=true` 并记 502，确认至少存在取消误分类。
  该样本不证明全部 502 同因，也不证明业务请求由谁取消。
- PR #3161 已有 Messages 取消归入 499 的实现及测试；证据采集时尚未合入，实施前已合入 `016bed1fe`。
  本轮复用该交付，不另起同义修复。观察到其 CI 单元/集成/preflight 均成功；
  这不是本人本地再次运行该 PR 全套测试的声明。
- 两地 outbox 快照均没有水位后的积压；两地数据库 trigger 仍含 FOR KEY SHARE。
  未观察到活动等待；不能据此证明没有晚提交事件丢失或汇总阻塞风险。

## Error Semantics

### Messages cancellation

按 [499 SSOT](client-closed-499-ssot.md) 消费既有判定 owner：调用方取消记 499，
不追加通用 502、不惩罚账号；DeadlineExceeded 和真实上游错误不能被改成 499。
PR #3161 是该子任务的现有交付载体。上线后另按新请求窗口核对真实 5xx，
不回写历史记录，也不把计数下降冒称供应商故障被修复。

### Chat SSE

复用 [failover SSOT](gateway-failover-policy-ssot.md) 和既有 Ops/终态 owner。
顶层 `error` 不是正常 Chat chunk，raw、buffered、Chat→Responses 路径必须共享识别。
role/空 content/usage-only 不算用户已收到业务内容。

| 状态 | 后续事件 | 结果 |
| --- | --- | --- |
| 尚无业务输出 | 可恢复的流内上游错误 | 保留未提交响应，交既有受限 failover；不另建重试预算 |
| 尚无业务输出 | 不可恢复的请求错误 | 复用现有协议映射，不盲目遍历账号 |
| 已有业务输出 | 任意失败或不完整 EOF | 显式失败终态，不重放请求，不伪装 completed |
| 正常终止 | 合法完整回答 | 保持成功，保留 usage |

SSE HTTP 200 本身不证明业务成功；buffered 非流式失败不得返回 completed。
错误体有界读取且脱敏；既有 usage/计费结算幂等不能因失败终态而丢弃或重复。
不将 Ops 错误分类和客户端错误清洗各复制一份状态机。
内部分类保留上游明确的认证、权限、限流及容量状态；这些数值不作为新增对客字段。
错误清洗及 SSE/WS 失败终态构造必须保留 `error.headers.retry-after`，
仅透传该 header 的字符串或数字值，不恢复其他 header 或嵌套诊断对象。
容量降载缺少等待指导时保持既有默认值；已有等待值不得覆盖。

## Outbox Delivery

替换 `id > Redis watermark` 作为完整性判据；ID 只可用作排序/诊断。
数据库行为采用持久 claim/lease/ACK，至少一次投递：

`pending -> leased(token, expiry) -> acknowledged`；失败或租约过期可再次领取。
领取用短事务和 SKIP LOCKED，不在账号/Redis 投影处理期间持有普通行事务锁。
ACK 必须匹配 claim token，失去租约的旧 worker 不能确认新 owner 的任务。
清理只删除已 ACK 事件；Redis 丢失或水位重置不能隐式确认数据库任务。

**去重边界必须保持生产者更新不丢失。** 不能简单把 dedup key 保留到 ACK，
否则 claim 后、处理前后发生的新更新会被 ON CONFLICT 丢弃。
建议在持久 claim 的同一事务中释放该事件的 dedup key，让后续变更产生新的 pending
事件；原 leased 行仍保留，可失败重试。若选 generation 合并方案，必须以 generation
匹配做 ACK 并保留期间的新变更，不能只有一个布尔 dirty 标志而不处理竞争。

实现继续读取实际账号/Plan。对于同一投影作用域的并行事件，需串行或重读当前事实并
证明较旧 worker 不能回写过期投影；不依赖跨进程的进程内 seen map。
毒事件不能静默删除：记录脱敏原因/次数/年龄，告警并触发既有受控重建策略。

迁移为新增字段/索引，旧 migration 不改；实施时确认新 migration 编号未被并行工作占用。
旧 reader 的水位 cleanup 与新 ACK reader **不能混跑**。发布需给出两地各自的 worker
单 owner 切换步骤与已排空证明，回退不得重新启用会删除未 ACK 行的旧 cleanup。
不能仅以“schema expand-only”宣称状态机切换可蓝绿兼容。

## Group Rollup

目标：普通当前日 usage INSERT 不等待耗时 rebuild；历史 INSERT/UPDATE/DELETE 和
发布仍保持金额一致性。不能仅删除 FOR KEY SHARE 或把 builder 锁降级就算修复。

沿用同一 state/rollup owner，建议：

- builder 用独立 advisory lock 串行化；不改变事务原子发布边界。
- 当前日写入走不与 builder 冲突的轻路径；历史写入必须与发布序列化并重新读取水位。
- 判定历史范围必须覆盖 builder **即将发布**的日期，而非仅旧 closed_before。
  特别覆盖“昨日写入在聚合 SELECT 后提交、旧水位仍为昨日”的竞争。
- 优先证明 FOR NO KEY UPDATE/历史写入专用锁的最小方案；如果当前日/历史的时区或
  日界线无法可靠区分，再引入显式 build cutoff/generation，不能无条件放行。
- 时区变更、DST、跨日进行中的 rebuild、删除级联及归档保留原有行为。

新增 migration 更新 trigger；普通表和分区表都验证。涉及数据库状态，因此本段须在
实现前确认；不能沿用上轮建议中“无锁读 + 条件更新”而遗漏新发布区间的迟到写入。

## Security Boundaries

### Codex tenant isolation and error envelope

在实际出站构造边界统一处理 HTTP/raw/WS 的 session/thread 安全作用域，至少区分
服务端认证的 API key、实际账号及客户端会话；不能信任客户端自行提供的租户标识。
同一调用方会话的多轮保持稳定，不因请求 ID 每次变化而断连续性。
保持 device/installation identity，不改变上游安全审查策略。

对客 response.failed/error 保留可用的 type/code 和安全消息，去除安全摘要的
detailed_explanation、steer.message 及嵌套同类字段；内部也不能默认记录完整原文。
测试允许的 code/type 保持兼容，不以删除整个错误事件的方式“修复泄漏”。

### Refresh rotation

保留现有 10 秒多标签页宽限体验，但同一旧 refresh token 的并发/重试返回同一 successor，
而不是每次新签一对。采用 Redis 原子 claim/CAS + 同一结果的有界重放。
必要重放材料的存储必须受服务端密钥保护，不落日志，不将 raw bearer 加入普通 metadata。

仅检查用户 active/version/binding 之后才能返回重放结果；grace 过期拒绝旧 token。
claim、结果保存或旧 token 失效失败时 fail closed；不得标记/删除错误后继续签发。
崩溃窗口不能使旧 token 长期有效；并发 worker 不能互相覆盖结果。
family revoke 必须清除关联重放材料，保留现有密码修改/禁用/绑定语义。
不把上游一次性 DEL 直接套入而重新造成多标签页被踢出。

### Redaction and prompt audit

在 `logredact` owner 统一规范化识别支付/cloud secret 字段的 snake/camel/kebab 形式，
覆盖结构化 map、JSON/SSE 和已识别 assignment，不承诺对任意自然语言做完整 DLP。
保持非敏感字段和普通文字，按既有 sentinel 同步 redaction_version。

prompt audit 默认不持久化/返回 FullPrompt，保留脱敏预览、hash 和决策。
移除 API DTO/UI 原文出口时同步协议和真实 UI 验证；两地当前开关关闭、事件表为空的
上轮证据不是删除其他环境历史数据的授权。本轮不修改 QA raw 归档 owner 或保留政策。

## Validation And Delivery

实施后必须将基线复现改为正确行为断言，纳入 CI，不长期保留断言缺陷成立的测试。

| 子任务 | 核心正向/负向/回归 |
| --- | --- |
| 取消归类 | wrapped Canceled/request canceled -> 499；DeadlineExceeded/真实上游故障仍真实分类；不惩罚账号 |
| Chat | role/usage-only + error、error-only、已有 text/tool 后 error、正常 DONE、EOF/读错/取消；流式和 buffered；usage 只结算一次 |
| Outbox | 小 ID 晚提交；处理中再次 enqueue；租约过期旧 token ACK；崩溃后接管；双 worker 同投影；清理不能删 pending；Redis 不可用 |
| Rollup | rebuild 中当前日 INSERT 及时完成；新发布区间的历史 INSERT 不丢；UPDATE/DELETE/级联；普通/分区表；时区/DST/午夜边界 |
| 会话/摘要 | 合成双租户隔离，同租户多轮稳定，HTTP/raw/WS；嵌套摘要不出站，合法错误码仍可读 |
| Refresh | 并发拿同一 successor；grace 边界；Redis 每个故障点；崩溃；禁用/改密/binding；family revoke |
| 脱敏/audit | canary secret 在所有已识别格式不泄漏；非秘密保留；无原文落库/DTO/UI；QA raw 生命周期不变 |

先复用 #3161 并区分其合并/部署状态；之后 Chat 错误语义和局部安全修补先交付，
outbox、rollup、refresh 各自保持可审查的原子实现和对应并发验证。
不因用户一次要求处理多项就将不同状态机塞进不可独立回退的巨型提交。

纯服务项采用单元/真实 PostgreSQL+Redis 集成测试，不冒称 e2e；涉及 prompt-audit UI
或真实多标签刷新工作流时用 Playwright。所有实现先 preflight 和独立 review，
合并/部署保持另外的人工确认；本审批不授予该权限。

## Implementation Notes

本节记录获批边界内的具体实现选择；不代表已合并、已部署或完成全部验收。

### Durable Delivery

`tk_113` 添加持久 claim/lease/ACK 字段与递增投影代次序列。单 owner 持有独立
PostgreSQL advisory session lock，领取后原子释放 dedup key，每条成功后单独 ACK。
旧水位只供诊断；待投递数量和最老时间均读取未 ACK 行。清理只认 ACK，数据库 trigger
拒绝删除未 ACK 行。失败保留事件，租约到期重试；不加入隐式死信删除。

仅取消 context 不能防住断连旧 worker。生产 Redis cache 在投影前安装数据库递增代次，
拒绝较旧或相同代次重新安装。账号、快照发布及桶生命周期写入通过同一 Redis 连接上的
WATCH + MULTI/EXEC 原子检查代次；新 owner 安装代次后，旧请求即使不取消也不能写入。
没有代次的原有非 outbox 调用保持原契约；本修复不声称消除了其他独立重建路径的既有竞争。
Redis fence key 不设置 TTL。禁止在线清除该 key、FLUSHDB 或回退数据库 sequence；恢复时
先停全部 worker，保证新序列高于保留 Redis 代次，再启动和强制全量重建。

### Dirty Days

不采用单纯降级 builder 行锁：跨午夜时，写入可能在聚合快照之后提交，仍会遗漏。
`tk_112` 为每个时区/日期保留 durable dirty marker。当前日 INSERT 不锁 rollup state，
但在日期 marker 上 `ON CONFLICT DO UPDATE` 持锁至 usage 事务提交。
builder 在聚合之前 DELETE/领取已可见 marker，按最小脏日期回退构建起点；未提交 marker
会在后续提交保留，读者按最小脏日期夹住有效水位、回读 raw。不能改成 DO NOTHING。
summary 的状态与 raw 查询使用同一 REPEATABLE READ 快照。
历史写入继续与 state 发布序列化，并检查将要发布的日期范围。保留原有修改/删除契约。
当前日并发写入仍会短暂竞争同日期 marker；发布前需观察写入吞吐及锁等待。

### Coordinated Cutover

以下是待审批的发布操作说明，本轮不执行。

1. prod 与 cn-prod 分别走现网部署 owner；留存旧镜像、备份和两地版本/数据库状态证据。
2. 对目标站点停止接入并排空在途 usage 写入，停止所有旧 app/worker。确认没有旁路 worker
   或共享库旧 reader；不能仅停止一个容器，也不能依赖 blue/green 混跑。
3. 单实例在停机窗口运行新增迁移；失败保持停止状态，排查后前向修复。禁止手工重写旧迁移。
4. 启动同版本实例，先完成 scheduler 全量重建与 group rollup 同步，再恢复流量。
   核对 pending/attempts/最老年龄、投影数据、raw 与 summary 金额及 usage INSERT 锁等待。
5. outbox 中迁移前留存行全部为 pending，按当前数据库事实重放；不按旧水位批量 ACK。
6. 回退时先停全部新实例。`tk_112` 生效后不能直接启用不认识 dirty marker 的旧 reader；
   `tk_113` 生效后也不能重新启用旧水位 cleanup。优先前向修复或使用保留新投递与 dirty-day
   reader 的兼容回退镜像；恢复旧 reader 需另行审批并证明 raw/rollup 和 pending 事件完整性。
7. refresh 新旧版本不能同时处理轮转：旧版 10 秒记录没有加密结果，新版会 fail closed。
   使用同一 JWT secret，切换排空后等待旧宽限窗口结束；极少数在途刷新可能需重试或重新登录。
   回退刷新实现也需排空，不能把旧版并发多 successor 行为带回混跑。
8. 未完成两地上线验证前，不将 issue tracking 的“线上已解决”勾选为完成。
