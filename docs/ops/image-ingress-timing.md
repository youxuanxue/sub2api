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
