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
| 公网 `curl https://api-<edge>.tokenkey.dev` **连接超时** | Lightsail 防火墙缺 **TCP 443**（基线 TCP 443 + 8443 + UDP 34567；SSM 内 curl 仍正常） | `aws lightsail get-instance-port-states`；`bash ops/stage0/verify-edge-lightsail-network.sh <id> --enforce-ports` |
| DNS 已指 Static IP 但 **TLS handshake 失败** / 证书错误 | provision 早于 DNS → ACME NXDOMAIN；Caddy 未续签 | DNS 生效后 `verify-edge-lightsail-network.sh <id> --renew-cert` |
| Edge SSM 找不到 instance | 仍按 EC2 CFN stack 查（edge 已全量迁 Lightsail，无 CFN） | `resolve-edge-deploy-route.py --json`；Lightsail 用 tag SSM `mi-*` |
