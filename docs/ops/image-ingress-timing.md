# Images 入口耗时排查

同步 `/v1/images/generations`、`/v1/images/edits` 及无 `/v1` 别名的
`http request completed` 日志附带以下数值字段。沿用该条日志的 `request_id`；
不采集 prompt、图片、凭证或新增请求体副本。

| 字段 | 含义 |
| --- | --- |
| `latency_ms` | 原有 HTTP middleware 总耗时 |
| `image_ingress_ms` | HTTP 计时开始至 `MaybeResolveUniversal` 返回，成功与失败均记录；不含后续 handler 排队/转发 |
| `image_ingress_key_auth_ms` | API Key 认证 middleware 进入至通用路由调用之前；提前拒绝也记录，不含后续分组计费检查 |
| `image_request_body_read_ms` | 原始请求体实际 `Read` 调用累计等待时间，包含上传等待；不计内存重放和解压时间 |
| `image_request_body_bytes` | 原始请求体实际读取字节数；部分读取失败也累计，不是解压后大小 |
| `image_request_content_length` | HTTP 请求声明长度；`-1` 表示未知，不能替代实际读取字节数 |
| `image_ingress_selection_ms` | 入口候选选择的总时间，包含下面查询评估、负载读取、排序、绑定等 |
| `image_ingress_candidates_ms` | 入口候选账号查询和可用性评估，包括关联数据库/Redis、计费准入检查 |
| `image_ingress_bind_ms` | 入口选中候选的计费上下文绑定与再次准入检查 |

这些是嵌套时段，不可相加。缺字段表示没有经过该阶段，`0` 表示观测时长不足 1ms。
入口选路返回后冻结选路计时，handler 后续选号、等待和重试不再累加；请求体读取独立计时，
因此绕过通用路由而在 handler 才读取请求体的 Direct 路径仍有上传证据。
没有读取请求体的鉴权拒绝会记录零字节，不会为了诊断提前消费上传。

排查顺序：先对比 HTTP 总耗时与入口耗时；入口偏高时看请求体读取、Key 认证、候选选择；
候选选择偏高时再看候选查询与绑定，剩余差值包含负载读取/排序等，不能擅称网络耗时。
请求体读取偏高说明服务端读入数据耗时，尚不能单凭它区分客户端、代理链路或服务端调度停顿。
这些字段不修改 `usage_logs.duration_ms`、`gateway_latency_ms` 或计费语义。

现有脱敏探针保留以上字段，例如：

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/probe-tail-gateway-logs.sh \
  --env REQUEST_IDS=<request-id> --env SINCE=1h
```

必须在包含本变更的镜像部署后采集新请求；不能追溯补出历史缺失时段，也不能用此日志
自动串联 prod 与 edge 的不同 request id。验证须经 prod 真实入口，不能只测 edge 本地转发。

## 上游与回传分段

OpenAI Images 转发额外输出 `component=audit.image_timing` 的两类日志，进入现有
`ops_system_logs` audit 通道；不新增数据库列，不采集 prompt、图片、URL、凭证或原始错误正文。

- `image.forward_timing`：一次账号转发的总耗时和写回耗时。`image_forward_index` 从 1 开始，
  handler 换号或同号重试会递增；`image_forward_started_at` 是本次转发的开始时间。
- `image.upstream_timing`：每次上游 HTTP 调用的观测。按
  `request_id + image_forward_index + image_attempt_index` 关联；同一次 forward 内的
  主模型 fallback、`n>1` 补图各有不同的 attempt。`image_attempt_start_ms` 相对 forward 开始。
  调用出错、body 读完/出错/关闭都会记录，EOF 后 Close 不重复记录。

### 网络与上游事件

下列里程碑字段均为**相对本次 HTTP attempt 开始的毫秒偏移**，不是可直接相加的阶段时长。

| 字段 | 观测点 |
| --- | --- |
| `image_dns_start_ms` / `image_dns_done_ms` | DNS 回调开始/结束 |
| `image_connect_start_ms` / `image_connect_done_ms` | 首次拨号开始/首次成功拨号结束 |
| `image_tls_start_ms` / `image_tls_done_ms` | TLS 握手开始/成功结束 |
| `image_got_conn_ms` | 获得连接；包括连接池等待、代理连接及握手 |
| `image_request_headers_written_ms` / `image_request_written_ms` | 请求头写完/整个请求写完 |
| `image_first_response_byte_ms` / `image_response_ready_ms` | 网络首个响应字节 / transport 返回可读响应（可能含解码初始化） |
| `image_first_body_byte_ms` / `image_last_body_byte_ms` | 首次/最后一次读到响应体字节 |
| `image_first_event_ms` / `image_response_created_ms` | 首个完整 SSE 数据帧 / `response.created` |
| `image_tool_started_ms` / `image_generating_ms` | 图片工具 in_progress（或 image item added）/ generating |
| `image_first_partial_ms` | 首个完整预览图片事件 |
| `image_tool_completed_ms` / `image_result_ms` | 图片工具 completed / 完整图片结果事件 |
| `image_response_completed_ms` / `image_upstream_failed_ms` | Responses 完成 / failed、incomplete 或 error 事件 |
| `image_body_end_ms` | body 读完、出错或被关闭；请求失败时是失败返回点 |

`image_body_end_reason` 区分 `eof`、`read_error`、`closed`、`request_error`、`no_response`、
`no_body`。HTTP 200/EOF 不代表成功出图，还须看 forward 的 `image_forward_failed` 与
`image_count`；`closed` 不代表读到了 EOF。

`image_connection_reused` 表示首次获取的连接是否复用；`image_connection_count` 和
`image_request_write_count` 暴露一次 HTTP 调用内部的多次连接/成功写请求。
若大于 1，底层可能发生透明重试或重定向，首个回调偏移不能当作唯一物理请求的完整分段。
自定义 transport、SOCKS 代理或复用连接不一定触发 DNS/TLS 回调；缺字段表示未观测，不能填零。

`image_upstream_request_bytes` 是请求声明的 ContentLength（不是实测上传字节数）；
`image_upstream_body_bytes` 是应用实际读取的响应体字节数（transport 解压后的大小，不是线上压缩字节数）。
`image_body_read_ms` 累加 Read 调用的等待时间，**包含等待生成，不能称为纯下载耗时**。

SSE 在 transport 返回的 body Read 时按实际行和帧分隔符识别，不依赖上游 Content-Type
（Codex 实测会省略该响应头）；客户端非流式时也有效。不会提前读取、额外请求或改变响应。
普通 JSON 及其中转义的 SSE 文本不会生成事件里程碑。
只保留有界的行前缀，图片正文不复制；超长行标记 `image_event_prefix_truncated=true`。
事件 type 在前缀之外或跨多行 JSON 时可能无法识别，不推测缺失里程碑。
时间戳在完整帧结束时记录，因此包含该事件内容的传输时间；没有预览事件就没有预览耗时。

### 回传与解释

`image_forward_ms` 是完整账号转发时间，包含准备、上游调用、结果转换和写回。
`image_downstream_write_ms` 累加同步 Write/WriteString/Flush 耗时，
`image_downstream_bytes` 是实际写入字节，`image_downstream_failed` 记录写失败。
非流式 JSON 保活心跳不计入；流式显式 keepalive 写入仍计入。
这些写入表示交给下游连接，不保证客户设备已收到。
非流式最后一次 attempt 正常 EOF 后，`image_forward_ms` 减去
`image_attempt_start_ms + image_body_end_ms` 是末次上游读取后的尾段，
包含解析、后处理、图片 offload 和回传，不能单称为转换 CPU 耗时。
流式读写重叠或后续 attempt 失败时不要套用该差值。

诊断先看入口上传和 handler 之前的阶段，再逐 attempt 对比：
连接获取 → 请求写完 → 网络首字节/响应可读 → 工具开始 → generating → 完整结果 → 回传。
工具开始前包含上游排队/主模型编排，generating 到结果包含上游处理和传输；
供应商未暴露的内部 GPU 计算与队列时间仍不能精确拆开。不要跨 retry 相减或重复累加嵌套区间。
非流式返回路径的既有 TTFT、usage duration 和计费字段保持原语义。

前述探针按 `REQUEST_IDS` 查询时，`related` 会保留这些分段字段；也可用
`--env MESSAGE_CONTAINS=image.upstream_timing` 收集 attempt 样本。
只有部署此代码后的新请求有这些观测，历史 QA 不会补出缺失时间。
