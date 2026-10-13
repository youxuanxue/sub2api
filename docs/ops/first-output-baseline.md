# 首包阈值七天基线

先保持现有阈值，部署新增观测后收集至少七个完整自然日。历史日志缺少字段时不回填猜测值，不与新增字段的样本混算。各节点分别统计，prod 和 edge 的同一次转发不能相加当作独立用户请求。

## 最小记录

- `gateway.first_output_budget`：真正启用预算时记录 `budget_ms`、`budget_reason`、`attempt_count`，以及已有的 request/model/platform/account 归因。标准、高 effort、compact、大请求体和 NewAPI 继承分开统计。
- `upstream_errors` 中的 `first_output_timeout`：保留已有 phase/elapsed/detail，增加结构化预算与原因；请求结束时补 `attempt_count`、`timeout_outcome`。
- `gateway.first_output_timeout_outcome`：每条发生首包超时的请求记录一次最终结果：`recovered`、`final_failure`、`client_cancel`。带内 SSE 错误也算失败，不能仅凭 HTTP 200 判恢复。

`attempt_count` 是请求内进入所选协议执行的次数，包括换号及 handler 发起的同账号重放；不把错误事件条数当作次数，也不把同一次执行内部的 TCP 重连、HTTP 底层重试或 WebSocket turn 当作新的协议执行。

预算与最终结果日志使用 `audit.first_output` 组件，复用现有 `ops_system_logs` 索引及保留策略；超时事件复用 `ops_error_logs.upstream_errors` JSON，不增加 schema。日志异步队列发生丢弃或监控关闭时必须标记窗口不完整。

## 每天回答四个问题

1. 按节点、模型、`budget_reason` 和实际 `budget_ms` 分层，多少请求/协议执行启用了这个阈值？
2. 多少请求被首包超时截断，累计多少超时尝试？
3. 被截断的请求最终恢复、失败、客户端取消各占多少？
4. 同层成功请求的 TTFT p50/p95/p99 和缺失率是多少？

关联以节点内 `request_id` 为准；不同 attempt 的预算可能不同，超时尝试用其事件自身的预算归因，不能用最后一次尝试覆盖。只统计明确存在新增预算字段的样本，缺失记为未知。WebSocket 连接包含多个 turn，单独展示，不能与 HTTP 单请求的分母混算。

当前 WebSocket 仅在超时事件中记录预算，不记录每个 turn 的预算启用日志；可统计超时事件及连接最终结果，不能据此计算 WebSocket turn 超时率。

现有成功用量 TTFT 与首包 guard 的完成条件并不相同；reasoning progress 可解除 OpenAI guard。超时样本已经被截断，不能推断继续等待一定成功，也不能用成功 TTFT 的 p99 单独决定新阈值。
