# Edge 模型族 429 健康告警设计

状态：对话设计已批准，书面 spec 待用户复核。

日期：2026-08-18

## 背景

现有 Edge 健康告警把两个不同范围的信号拼在一起：账号查询只看 `platform=anthropic`，流量却来自整个 Edge 容器的全平台 Docker 日志。这样得到的 `served_200:no_available_429` 比例不是 Claude 成功率，账号数也遗漏能承载 Claude 模型的 Kiro 账号。结果会把“某个 Edge 的 Claude 路由正在拒绝请求”错误表述为“整个 Edge 宕机”。

us5 的现网证据说明了这个问题：Anthropic 账号为零，唯一 Kiro 账号因额度耗尽不可调度，但 Grok 账号仍正常；近两小时最终空池拒绝只来自 Claude 模型。因此告警单元必须从“Edge 账号库存”改为“Edge × 模型族的真实终态请求结果”。

Edge 返回 429 也不自动等于最终客户掉单。prod 可能通过其他 Edge failover 成功。Edge 告警描述局部路由受损；最终客户影响继续由 prod 的 `user_visible_failure_count` 负责确认。

## 目标

- 以 `edge × 模型族` 为独立告警单元。
- 只用客户端从该 Edge 收到的最终空池 429 驱动容量告警。
- 保留精确 model 计数，并在一条模型族通知中列出主要受影响模型。
- 仅在有实际拒绝且达到高精度阈值时通知。
- 用完整的五分钟聚合桶实现持续触发、升级、恢复和去重。
- 保留主机不可达告警、prod 用户体验告警和手工账号库存巡检。
- 不给网关请求热路径增加同步 I/O、网络调用或新的逐请求明细写入。

## 非目标

- 不因零账号、单账号或账号池为空而主动通知；没有最终拒绝就不创建告警状态。
- 不用本告警判断 prod 的最终客户是否掉单。
- 不替换 `channel_monitor_v2` 管理台、历史聚合或 prod 告警体系。
- 不新增独立时序数据库、指标代理或逐请求事件表。
- 不把所有模型映射复制到运维脚本中。
- 不改变账号调度、cooldown、failover 或请求状态码行为。

## 已确认决策

### 职责边界

- `channel_monitor_v2` 保留，作为结构化聚合数据源。
- prod 的 `user_visible_failure_count` 保留，作为最终用户体验兜底。
- `edge-health-watch` 保留 HTTPS/SSM fleet 编排和主机不可达路径。
- `edge-health-watch` 中基于账号数和全平台混合流量的 `down/degraded` 通知判定由模型族状态机替换。
- `thin`、单账号和零账号态只保留在手工巡检中，不进入飞书正文、严重度或去重状态。
- prod 可以继续参与主机探测和手工诊断，但不能创建 Edge 模型族状态；prod 的模型失败只由 prod 告警体系通知。
- 旧 `routing_capacity_rejection_count` 默认规则已经由 `tk_061` 删除，不恢复。
- Edge 节点的 prod-only 用户体验规则和平台池即时 P0 已有运行时抑制，继续保持。

### 严重度阈值

- 橙色降级：连续两个完整五分钟桶均满足最终空池 429 数量至少为 10，且占该模型族全部终态请求的比例至少为 20%。
- 红色不可用：任一完整五分钟桶最终空池 429 数量至少为 50；或者连续两个完整桶的空池 429 比例均至少为 80%。
- 恢复：连续三个完整五分钟桶均满足最终空池 429 数量小于 5，且比例小于 10%。
- 红色优先于橙色。红色回落到橙色区间不发送降级通知，事件保持 active，直至满足恢复条件。

## 数据设计

### 精确错误分类

在 `channel_monitor_v2` 的版本化错误 taxonomy 中增加 `final_empty_pool_429`。分类必须同时满足：

- `error_phase = 'routing'`
- `status_code = 429`
- `is_count_tokens = false`
- 记录是同一 `request_id` 的最终错误记录

该分类排除：

- 上游账号返回的 429
- failover 或重试过程中的中间失败
- 最终已恢复为成功的请求
- provider 级普通限流或容量错误
- unsupported-model 400
- count-tokens 请求
- `/health` 等不进入网关请求观测链的探测

鉴于分类集合发生变化，taxonomy version 从 1 提升到 2。Go 分类器和 SQL 聚合器必须保持同序、同语义。现有表结构已经支持新的 category 和版本，无需数据库 schema migration。

现有错误聚合的去重 CTE 需要携带 `error_phase`，并在宽泛文本分类 `account_pool_unavailable` 之前识别精确分类。原 `account_pool_unavailable` 继续用于管理台诊断，但不能驱动本告警。

### 聚合字段

继续复用现有粒度：

```text
1 minute × platform × group_id × exact model
```

五分钟告警读取既有 `bucket_seconds=300` rollup。每个精确 model 至少提供：

- `success_requests`
- `error_requests`，即全部终态错误
- `final_empty_pool_429`

模型族的空池比例定义为：

```text
sum(final_empty_pool_429)
-----------------------------------------------
sum(success_requests) + sum(error_requests)
```

分母为零时比例为 unknown，该桶不满足任何触发或恢复条件。聚合仍按精确 model 保存，只有告警判定时才上卷到模型族。

### 模型族归类

模型族以客户端请求的精确 model ID 为主，不以最终承载账号的 provider platform 为主。`requested_model` 优先，缺失时回退 `model`。

- `claude-*` 归入 Claude 模型族，无论承载账号属于 Anthropic 还是 Kiro。
- 其他已知模型通过与 `service.DetectModelPlatform` 同源的 owner/helper 归类。
- 运维 Python 不维护第二份完整模型表。需要跨语言消费时，由 owner 导出最小、可测试的规则或生成 fixture，并用 sentinel 防止漂移。
- 无法归类的 model 进入 `unknown` 诊断。`unknown` 不直接 page，也不写入 active 告警状态。

### 账号诊断

账号库存只在模型族已经触发后用于解释，不参与阈值、严重度或去重。

Claude 诊断使用现有 Edge OAuth 资格口径，联合查询 `platform IN ('anthropic','kiro')`，并保留各平台可调度数。其他模型族使用各自 owner 的平台映射。诊断查询失败不阻断指标告警。

## 采集与数据契约

`probe-edge-health.sh` 不再以 Docker 全量日志作为飞书容量判定数据源。远端只读探针通过一次 PostgreSQL 会话返回：

- `channel_monitor_v2_watermarks` 的 `data_through` 和 `last_successful_at`
- 最近尚未评估的完整五分钟桶，至少覆盖触发和恢复所需窗口
- 每个桶、platform 和精确 model 的成功数、全部终态错误数和 `final_empty_pool_429`
- 可选账号诊断

fleet JSON 每个目标一行，使用字段名而非位置解析。概念结构如下：

```json
{
  "edge": "us5",
  "host": {"reachable": true, "http_code": 200},
  "monitor": {
    "fresh": true,
    "data_through": "2026-08-18T14:31:00Z",
    "taxonomy_version": 2
  },
  "buckets": [
    {
      "bucket_start": "2026-08-18T14:20:00Z",
      "platform": "kiro",
      "model": "claude-sonnet-4-6",
      "success_requests": 43,
      "error_requests": 18,
      "final_empty_pool_429": 18
    }
  ],
  "diagnostics": {
    "accounts": {"anthropic_schedulable": 0, "kiro_schedulable": 0}
  }
}
```

当前未关闭的五分钟桶不能参与计算。一个桶只有在以下条件同时成立时才合格：

- 桶结束时间不晚于最近安全聚合边界
- `data_through` 已覆盖桶结束时间及一分钟迟到写入余量
- 桶时间与相邻桶严格连续
- taxonomy version 为当前版本

GitHub Actions 的运行次数不参与“连续窗口”定义。workflow 延迟或漏跑后，evaluator 从数据库桶时间重建连续序列。

手工 `scan-edge-health.sh` 输出保留主机状态、模型族指标和账号库存姿态。旧的全平台 Docker `served_200:no_available_429` 比例不再生成 `down/degraded` verdict，避免自动通知修正后手工工具仍给出相反结论。

## 状态机

每个 `edge × model_family` 独立维护以下状态：

- `inactive`
- `degraded`
- `unavailable`

### 触发

从 `inactive` 开始：

- 最新单桶数量达到红色数量阈值时，直接进入 `unavailable`。
- 最新两个连续桶均达到红色比例阈值时，进入 `unavailable`。
- 否则，最新两个连续桶均达到橙色数量和比例阈值时，进入 `degraded`。
- 数据不足、桶有缺口、分母为零或未达到阈值时保持 `inactive`。

首次启用时，单桶红色数量规则可立即触发；所有持续规则仍要求完整的连续桶。

### active 期间

- `degraded` 达到红色条件时升级为 `unavailable` 并通知一次。
- `unavailable` 回落到橙色区间时保持 `unavailable`，不发送降级消息。
- 普通指标波动不重复通知。
- 同一个最新桶只能处理一次，不能因 workflow 重跑重复推进状态。

### 恢复

active 状态只有在最新三个连续完整桶均同时满足恢复数量和比例条件时才回到 `inactive`。恢复发送一次通知，然后清除该 pair 的 active 状态。

没有请求、没有空池拒绝、只有零账号或薄边姿态时，不创建 pair 状态，也不进入去重 key。

## 去重状态

现有扁平字符串 key 改为结构化 JSON。每个 pair 至少保存：

- 当前状态和最后已通知严重度
- 事件开始桶
- 最后评估桶
- 最后成功通知时间
- 最后已通知的主导精确模型集合

主导模型集合固定为最新桶中 `final_empty_pool_429 > 0` 的 Top 3 精确模型，按数量降序、model ID 升序稳定排序。active 期间只有以下变化允许发送“影响范围变化”更新：

- 新模型进入主导集合，且占该模型族最新桶空池 429 的比例至少为 25%；或
- Top 1 被另一个模型替代，且新 Top 1 占比至少为 25%。

普通 Top 3 排名交换不通知。严重度升级优先于影响范围变化，同一轮只生成一次更新。

状态仍由 GitHub Actions cache 承载，避免为告警引入新的云端写权限。缓存丢失或损坏时，从可用完整桶重建；当前满足触发条件则发送一次当前状态通知。该选择可能造成一次重复，但不会静默漏报。

## 通知设计

单个 pair 变化时直接命名节点和模型族：

```text
🟠 us5 · Claude 模型族路由降级
🔴 us5 · Claude 模型族路由不可用
```

同一轮多个 pair 变化时合并为一条 fleet 通知，按红色、橙色、恢复排序。每个异常项展示：

- Edge 和模型族
- 最近相关连续桶的 `空池 429 / 全部终态请求 / 比例`
- Top 受影响精确模型及空池 429 数量
- 可用时展示账号诊断
- 明确下一步：补充对应模型族的健康承载账号，或从该 Edge 摘除该模型族路由

示例：

```text
us5 · Claude
最近两桶: 13/52 (25.0%) -> 18/61 (29.5%)
Top 受影响模型:
  claude-sonnet-4-6  12
  claude-opus-5       4
  claude-haiku-4-5    2
诊断: Anthropic 可调度 0，Kiro 可调度 0
```

通知不使用“Edge 宕机”或“最终客户已受影响”。prod 的最终失败告警可以独立说明客户影响。

零账号、薄边、单账号和零请求信息不进入飞书正文。它们继续由 `scan-edge-health.sh` 的手工输出承担。

## 主机与监控故障

主机和遥测故障使用独立状态命名空间，不伪装成模型族故障。

| 条件 | 行为 |
| --- | --- |
| HTTPS 传输不可达 | 立即发送红色 `Edge 主机不可达`，保留既有恢复闭环 |
| HTTPS 正常但 SSM、PostgreSQL 或结构化探针不可用 | 不推进模型状态；连续两个不同的五分钟 watch slot 失败后发送橙色 `监控数据不可用` |
| 聚合水位陈旧或未覆盖完整桶 | 不推进模型状态；按监控数据不可用处理 |
| taxonomy version 不匹配 | 拒绝使用该数据；按监控数据不可用处理 |
| 账号诊断失败 | 模型告警照常，诊断标记为暂不可用 |
| 单个 target 输出格式非法或目标集合不完整 | workflow 失败，不提交任何新状态 |
| 已有模型事件期间监控中断 | 保持 active，禁止发送恢复 |

同一五分钟 slot 内的 workflow 重跑不能累计监控失败次数。监控数据恢复且水位新鲜后，一个有效 scan 即发送一次恢复消息。主机不可达与监控数据不可用各自去重，不污染模型族状态。

## 投递一致性

- workflow 使用单实例 concurrency，禁止两个运行并发推进状态。
- 只有飞书返回应用级成功后才原子提交新状态。
- 飞书失败时保留旧状态，下轮重复投递相同 transition。
- dry-run 不写状态。
- 无需通知但评估成功时，可以推进 `last_evaluated_bucket`。
- 有效的主机或遥测故障记录属于可评估结果；无法解释的部分扫描才使整个 workflow 失败。

## 性能边界

本设计不增加请求热路径工作。失败请求继续走现有异步 `ops_error_logs` 队列，成功请求继续走现有 `usage_logs`。新增成本仅包括：

- 现有后台聚合 SQL 中一个高优先级精确分类分支
- 现有聚合表中的少量 category 行
- 每个 Edge 每五分钟一次小范围、索引支持的只读查询

巡检频率从每十五分钟提升到每五分钟，SSM 命令数约为原来的三倍；每次命令不再拉取和解析两小时 Docker 日志，因此单次 CPU、I/O 和网络负担下降。

上线门禁：

- 每个 Edge 的聚合水位延迟小于两分钟。
- 每个 Edge 的告警 SQL 在两秒内完成。
- 最近窗口聚合不得逼近现有 55 秒执行超时。
- 若某个 Edge 尚未启用 `channel_monitor_v2`，不得切换该 Edge 的告警数据源。

## 测试设计

### Go 分类与聚合集成测试

- routing 阶段最终 429 计入 `final_empty_pool_429`。
- 上游 429、provider 429、recovered-200、unsupported-model 400 和 count-tokens 不计入。
- 同一 `request_id` 多条记录只采用最终记录。
- 1 分钟事实与 5 分钟 rollup 的成功数、错误数和精确空池数一致。
- 最近十分钟重算可以吸收延迟写入。
- Go 与 SQL taxonomy 对相同 fixture 返回同一 version 2 category。

### 模型族与状态机单元测试

- Anthropic 和 Kiro 承载的 `claude-*` 均归入 Claude。
- 精确 model 保留，`unknown` 不 page。
- 单桶数量 9 不触发；连续两个桶数量 10 且比例 20% 触发橙色。
- 单桶数量 50 立即红色。
- 连续两个桶比例 80% 触发红色。
- 不连续桶不能组合触发。
- 恢复严格要求三个桶数量小于 5 且比例小于 10%。
- 红色不降级，橙色只升级一次。
- 新主导模型占比恰好 25% 时发送一次影响范围更新。
- 分母为零、零请求和零拒绝不创建状态。

### 状态、投递与编排测试

- 重复扫描同一桶不重复通知。
- 多 pair transition 合并为一条通知。
- 飞书失败、dry-run、坏 JSON、部分扫描和水位陈旧不错误推进状态。
- 状态缺失或损坏时可从完整桶重建。
- HTTPS、SSM、数据库、账号诊断失败进入各自设计路径。
- workflow 使用五分钟 cadence、串行执行和结构化聚合数据源。
- 旧 Docker 全平台计数不再进入飞书容量判定。
- 纯函数 selftest 和 workflow contract test 接入 `scripts/preflight.sh`。

### 现网验收

- 所有 Edge 先 dry-run，用最近窗口逐模型对照原始 `ops_error_logs` 抽样。
- 用历史 us5 数据回放，必须只得到 `us5 · Claude 模型族` 异常；Grok 流量和账号不得混入。
- 零账号且零请求 fixture 必须产生零通知和零状态。
- 验证 prod 用户体验告警、Edge 主机不可达和手工账号库存巡检没有回归。
- 对比切换前后的聚合作业耗时、PostgreSQL CPU 和探针执行时间，满足性能边界后再启用正式投递。

## 切换顺序

1. 发布 taxonomy version 2 和精确聚合逻辑。
2. 验证所有 Edge 的 `channel_monitor_v2` 已启用、水位新鲜，并积累恢复判定所需的完整桶。
3. 新 evaluator 以 dry-run 运行，并与原始 `ops_error_logs` 抽样对账。
4. 一次性将 `edge-health-watch` 的容量判定切换到模型族状态机，同时保留主机不可达路径。
5. 账号库存驱动的容量判定退出飞书通知；手工巡检继续保留。
6. 观察聚合延迟、数据库负载、状态推进和通知内容，确认门禁后结束切换。

## 验收标准

- us5 存在健康 Grok 账号但 Claude 最终空池 429 达阈值时，只报告 Claude 模型族路由异常。
- Edge 某模型族无账号且没有最终拒绝时，不发送通知、不创建状态。
- Kiro 承载的 Claude 请求与 Anthropic 承载的 Claude 请求进入同一模型族。
- 上游和中间重试 429 不得推高最终空池计数。
- GHA 延迟不改变连续桶语义。
- 数据缺失不能产生假恢复。
- active 事件只在首次触发、严重度升级、主导模型显著变化和恢复时通知。
- prod 最终用户体验告警与 Edge 局部路由告警保持独立职责。
- 网关请求热路径没有新增同步 I/O。
