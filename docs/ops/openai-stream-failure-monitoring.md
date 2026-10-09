# OpenAI 流失败监测

HTTP 200 只表示响应头已收到。排查流失败时同时检查终态、用量和传输日志；缺少失败记录不等于已经恢复。

## 传输日志

`upstream_transport_failure` 在响应头请求失败或 Body.Read 失败时记录。普通传输和 TLS 指纹传输共用同一 owner：`backend/internal/repository/http_upstream_observation.go`。请求上下文有 request_id/client_request_id 时沿用，账号 ID 始终显式记录。

| 字段 | 含义 |
|---|---|
| `failure_stage` | `response_headers` 或 `body_read` |
| `failure_kind` | `http2_stream_reset`、`http2_connection_lost`、`http2_goaway`、`timeout`、`unexpected_eof` 或 `transport_error` |
| `http2_stream_id` / `http2_error_code` | 单流重置的流编号和错误码，不推测对端内部原因 |
| `upstream_protocol` / `alpn` | 已收到响应时的实际协议与 TLS ALPN；非 TLS 无 ALPN |
| `protocol_mode` / `proxied` | 连接池配置的协议模式、是否配置代理 |
| `connection_reused` / `connection_idle_ms` | 最后一次取得连接时的复用信息与空闲时长，不是连接年龄 |
| `peer_address` / `upstream_host` | 实际连接对端和目标主机名；经代理时对端可能是代理 |
| `body_bytes_read` / `elapsed_ms` | 已交给调用方的解压后字节数、该次尝试的耗时；字节数不代表已经下发给用户 |

每次尝试最多记录一次读取终态。正常 EOF、主动关闭和取消不记失败；提前 Close 不算成功。已有 OpenAI HTTP 代理回退的健康窗口改为由完整读取 EOF 清零，兼容性读取错误进入原有阈值；直连不因此启用 H1 回退。

`upstream_http2_transport_error` 接收 HTTP/2 transport 的 CountError 事件，字段为 `protocol_mode`、`http2_error_kind`。例如 `conn_close_lost_ping` 可与连接失联关联。它是连接级诊断，不能加到用户请求失败总数，也没有可靠的单请求归属。

日志不记录请求/响应正文、完整 URL、鉴权头、代理 URL 或原始错误字符串。事件使用现有日志管线；Ops sink 启用时 warning 可在系统日志检索。日志采样、sink 丢弃与保留期限仍适用，日志计数不是精确计费或请求计数器。

经 `ops/observability/run-probe.sh` 在目标环境执行有界只读 SQL，可查看最近传输故障分布：

```sql
SELECT host, account_id,
       extra->>'failure_stage' AS stage,
       extra->>'failure_kind' AS kind,
       extra->>'http2_error_code' AS code,
       count(*) AS events
FROM ops_system_logs
WHERE created_at >= now() - interval '30 minutes'
  AND message = 'upstream_transport_failure'
GROUP BY 1, 2, 3, 4, 5
ORDER BY events DESC
LIMIT 50;
```

先按 host/account/kind 聚合，再按 request_id 关联上层错误及用量。若出现 `http2_stream_reset`，比较复用连接与新连接、直连与代理、出口和账号；若出现 `http2_connection_lost`，关联同时间的 lost-ping 诊断。H2/H1 对照须保持相同账号、出口和工作负载，仅小范围执行。不能因一个 RST_STREAM 关闭全部并行流，也不能伪造完成事件。

## Protection 回归边界

OAuth Responses 历史含 `web_search_call`、无搜索声明且没有可执行工具（或显式 `tool_choice:none`）时，补只用于历史兼容的 cached-only 搜索声明并保持 `tool_choice:none`。Lite 使用 `additional_tools`，保留末尾 `compaction_trigger`；API-key 和 legacy `/responses/compact` 不应用此修复。已有活跃工具集合及强制工具选择保持原样。

精确 `response protection is unavailable` 被视为跨账号共享故障，经既有 failover SSOT 停止换账号；保留上游 5xx/失败终态，不改成客户端 400。该修复消除服务端换号放大，不对客户端独立重复请求实施去重；QA 的截断脱敏哈希不可用作幂等键。

发布后用原失败形状检查标准 Responses 和 Lite 的成功终态及对应账号用量，并比较每个失败入站请求的 failover 事件。不能只看 HTTP 200 或 probe 的 `uncorrelated_success` 标签。上游参考：[sub2api #7939](https://github.com/Wei-Shaw/sub2api/pull/7939)、[CLIProxyAPI #4011](https://github.com/router-for-me/CLIProxyAPI/issues/4011)、[Go #69963](https://github.com/golang/go/issues/69963)。
