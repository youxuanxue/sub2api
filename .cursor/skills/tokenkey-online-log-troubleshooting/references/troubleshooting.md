## 9) 常见失败与固定处理

| 现象 | 常见原因 | 固定处理 |
|---|---|---|
| `No such container: tokenkey-app` | 容器名猜错 | 先 `docker ps`，以 live 名称为准。 |
| `column ... does not exist` | schema 演化或表名猜错 | 先查 `information_schema.columns`，改 SQL，不继续猜。 |
| SSM success 但 stdout 空 | JSON/heredoc quoting 未执行预期 | 改成 `psql -c` 或远端 Python wrapper；读 invocation JSON。 |
| SSM 输出截断 | dump 太大 | 聚合优先，limit/tail，远端写文件只回摘要。 |
| `WITH ORDINALITY` 报错 | JSONB lateral 写法错 | 用 `CROSS JOIN LATERAL jsonb_array_elements(...) AS e(value)`；需要 ordinality 时 `WITH ORDINALITY AS e(value, ordinality)`。 |
| 误把 recovered upstream error 当故障 | 只看 `upstream_errors` | 必须同时看 final `status_code`。 |
| 单窗口却 429 | CLI 内部多请求/多模型短峰 | 解析 access log by-minute，而不是按窗口数量判断。 |
| CI 查错 run | branch/run 未定位 | `gh pr checks` → `gh run view`，按 PR head SHA / branch 过滤。 |
| `probe-gateway-ua-tls-compare` DB 窗太宽 | 未设 `WINDOW_MINUTES`，outage 证据被 LIMIT 稀释 | 根据 issue 换算分钟数， `--env WINDOW_MINUTES=N`。 |
| `fetch-gateway-debug-log` SSM Failed | debug 文件不存在或 env 未开 | 远端 `docker exec tokenkey test -f /app/data/gateway_debug.log`；无文件则勿拉 body。 |
| S3 presign / curl PUT 失败 | 实例无外网或桶策略 | 读 SSM stderr；检查 `SSM_OUTPUT_S3_BUCKET` 与 IAM；勿改线上只为 bypass。 |
| 公网 `curl https://api-<edge>.tokenkey.dev` **连接超时** | 活边 Lightsail 网络 / Caddy 监听 / DNS 不一致 | 先由现网 resolver 核对目标 IP 与 `mi-*`，再查 Lightsail 防火墙与主机监听；历史 Hcloud 边勿当活目标 |
| DNS 已指 Static IP 但 **TLS handshake 失败** / 证书错误 | provision 早于 DNS → ACME NXDOMAIN；Caddy 未续签 | DNS 生效后 `verify-edge-lightsail-network.sh <id> --renew-cert` |
| Edge SSM 找不到 instance | 目标/区域漂移或 Hybrid 离线 | `edge_ssm_execution.py --edge-id <id> --format json`；使用返回的 SSM region 与 `mi-*` |
