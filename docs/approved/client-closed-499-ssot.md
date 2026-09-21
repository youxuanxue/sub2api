---
title: Client-Closed 499 SSOT（客户端断开分类单一事实源）
status: approved
approved_by: "user (2026-09-15 conversation: 复审 #2179 后确认 499 分类未达全仓库 SSOT，批准在 #2179 分支上直接追加收敛修复)"
approved_at: 2026-09-15
created: 2026-09-15
authors: [claude]
risk: medium
---

# Client-Closed 499 SSOT（客户端断开分类单一事实源）

> 状态：approved。范围：全仓库全 TokenKey 服务对「调用方断开 / 取消」的错误分类、HTTP 499 归类与 ops 日志归属。
> 本文只记录 owner 与边界；实现细节以代码为准。

## 背景

同一个「客户端断开」错误（`context.Canceled`、请求 context 取消、lib/pq 57014 文本）曾在
middleware、handler、ops 分类器各有一套平行判定，语义不一致：同一个错误在鉴权路径判 499，
在 body 读取路径可能落到 400，在 ops 日志可能计入平台故障。PR #2179 复审后（2026-09-15），
判定谓词与 499 常量收敛到 service 层单一 owner。

## Owner 表（唯一枚举）

| 关注点 | Owner | 边界 |
| --- | --- | --- |
| 判定谓词（调用方断开） | `backend/internal/service/client_closed_request_tk.go` → `IsClientClosedRequest(c, err)` | 全部 ingress（API key 鉴权、universal 路由、body 读取、并发抢槽、ops realtime、terminal outcome）必须消费它，禁止再写 `errors.Is(err, context.Canceled)` 变体 |
| 499 状态常量 | 同文件 → `StatusClientClosedRequest = 499` | `middleware.StatusClientClosedRequest` 与 `handler.statusClientClosedRequest` 是 alias，不是第二定义 |
| pq 取消文本 | 同文件 → `PostgresCanceledByCallerMessage` | lib/pq 57014 不包装 `context.Canceled`，文本匹配是唯一信号 |
| gin 标记 / 读取 | `service/ops_upstream_context.go` → `MarkOpsClientClosedRequest` / `HasOpsClientClosedRequest` | 所有写入方必须消费，禁止裸 `c.Set` |
| ops 日志归类 | `handler/ops_error_logger.go` → `classifyOpsErrorLog`：`status==499 || (clientClosedRequest && !upstreamError)` → `phase=request` | 单一汇聚点 |
| Forward 期 499 终结 | `handler/client_closed_request_tk.go` → `markClientClosedForwardRequest` | failoverClientGone 与直接 Forward fallback 复用 |
| upstream 维度取消 | `handler/ops_error_logger_tk_client_canceled.go` → `tkUpstreamClientCanceled` | 独立维度（issue #625），语义上与 ingress 谓词不同：需排除终态上游 HTTP 状态 |

## 谓词语义（优先级，不得分叉）

1. `errors.Is(err, context.DeadlineExceeded)` → **不是**客户端断开（服务端 deadline 是平台故障，即使 pq 在解退时报告取消）
2. `errors.Is(err, context.Canceled)` → 是
3. 请求 context：`DeadlineExceeded` → 否；`Canceled` → 是
4. err 文本含 lib/pq 57014 取消文本 → 是

## 有意分歧（文档化，不是债务）

以下判定**故意**不消费 `IsClientClosedRequest`，修改它们前先读这里：

- `failover_loop.go` `failoverClientGone` / `HandleSelectionExhausted`：用 `ctx.Err() != nil`
  停止重试（Canceled **和** DeadlineExceeded 都必须停）——用已取消/deadline 的 context
  重新选号只会得到取消错误并被误报成账号耗尽。状态终结分流：
  `Canceled` → 499 / `MarkOpsClientClosedRequest`；`DeadlineExceeded` → 响应未提交时写
  504（平台超时），不得冒充 client-closed，也不得留下默认 200。
- `service/gateway_upstream_transport_error.go` / `openai_upstream_transport_error.go`：
  upstream 维度的 client-gone 判定（`err` 或 `ctx.Err()` 是 Canceled；deadline 只有在
  `ctx.Err()` 同为 deadline 时才算 client gone）。这是「上游传输层是否 failover/evict」的问题，
  不是「ingress 是否 499」的问题。
- service 层约 40 处流式读取的 `!Canceled && !DeadlineExceeded` 抑制性检查：语言习惯层面的
  错误抑制，不承担分类职责。
- `handler/ops_error_logger_tk_client_canceled.go`：见 owner 表。
- NewAPI chat：`relay/bridge.DispatchChatCompletions` 对 chat 一律启用
  `WithUpstreamRequestContext`；candidate bounded attempt 在
  `candidate_chat_attempt_tk.go` 上于客户端取消时（含首包后）取消上游 ctx，以便及时停流
  并释放账号并发。首包超时仍只在 `!started` 时中止以便换号。

## 已知边界（非 owner，记录在案）

- SQL 侧分类：`repository/channel_monitor_v2_aggregation.go` 与
  `service/channel_monitor_v2_error_taxonomy.go` 用 `status_code = 499 OR text LIKE …`
  重做分类。文本条件与 Go 侧 owner 的 pq 文本常量语义对齐，但物理上是第二套实现；
  修改 `PostgresCanceledByCallerMessage` 时必须同步检查这两处。
- `pkg/errors/types.go` `ClientClosed()`/`IsClientClosed()`（499 语义的应用层错误）与
  `pkg/googleapi/status.go` 的 499 分支：响应构造层面的既有设施，不承担 ingress 分类。

## 机械锚点

- Sentinel：`scripts/sentinels/gateway-tk.json` 锚定
  `service/client_closed_request_tk.go`（owner 本体）、`service/client_closed_request_tk_test.go`
  （优先级回归）、`handler/client_closed_request_tk.go`（谓词必须保持委托）、
  `server/middleware/middleware.go`（谓词与常量必须是 alias/委托）。
- 回归测试：`TestIsClientClosedRequest` 覆盖 deadline 优先级、wrapped Canceled、
  请求 context、pq 文本、statement timeout 反例。
