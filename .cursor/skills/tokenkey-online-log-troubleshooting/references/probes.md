## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。这张表是未来 PR 编辑本 skill 时 reviewer 的核对抓手：新增「步骤」必须先按此分类。

| 步骤 | 类型 | 承载 |
|---|---|---|
| 解析 prod target（region / instance_id / domain） | 机械 | `ops/stage0/resolve_prod_ssm_target.py` |
| 解析 cn-prod SSM 身份（region / managed instance） | 机械 | `deploy/domestic/prod-target.json`（由 `run-probe.sh --target cn-prod` 读取） |
| SSM base64 投递 + send-command + poll | 机械 | `ops/observability/run-probe.sh --target prod\|cn-prod --script <probe.sh>`（cn-prod 走 Hybrid managed instance，不开放公网 SSH） |
| `ops_error_logs` 标准聚合（schema + by_status + upstream_events，保留 reason/截断 message + 429-by-minute） | 机械 | `ops/observability/ops-error-triage.sh`（通过 run-probe.sh 投递） |
| final-429 / 5xx 分类（config-cap vs 空池 #575 vs 真上游：by error_type/owner/phase + by group·model·account + 5min 桶） | 机械 | `ops/observability/probe-429-classify.sh`（通过 run-probe.sh 投递；`WINDOW_HOURS` 默认 3；triage 后的分类深挖） |
| SLA Dashboard 等价拆解（success/error_total/error_sla/client_faults + by_status owner 口径 + top SLA messages） | 机械 | `ops/observability/probe-sla-breakdown.sh`（通过 run-probe.sh 投递；`WINDOW_HOURS` 默认 24；对齐 Admin Ops `error_owner` SLA 公式） |
| 每日错误账本（SLA totals + final/recovered 分离 + new/regressed/persistent + access-log capture gap + repair eligibility） | 机械 | `ops/observability/probe-daily-error-ledger.sh`（只读采集）→ `daily_error_report.py build/aggregate/select`（脱敏、分类、稳定签名）；由 `ops-daily-diagnostics.yml` 调度，代码修复只交给无 AWS 权限的 `ops-repair-draft.yml` |
| ⚠写侧止血：恢复 Anthropic OAuth 可调度并补 group_id（before/after 自证） | 机械(写) | `ops/observability/remediate-schedulable-pool.sh`（经 run-probe 投递；§10 交接修复时用，非只读、须先有结论） |
| ⚠写侧：统一 OpenAI OAuth `accounts.concurrency`（`APPLY=yes-openai-oauth-concurrency`，默认 25；图文共用该槽） | 机械(写) | `ops/observability/remediate-openai-oauth-concurrency.sh`（经 run-probe 投递；须授权；before/after 自证） |
| ⚠写侧：关闭进程级生图闸（`APPLY=yes-disable-image-concurrency`；`.env` upsert `GATEWAY_IMAGE_CONCURRENCY_ENABLED=false` 并 recreate 活动色容器） | 机械(写) | `ops/observability/remediate-disable-image-concurrency.sh`（经 run-probe 投递；须授权；会短暂重启 app） |
| ⚠写侧：cn-prod 2C/4G 应用池上限（`APPLY=yes-tune-cn-prod-pools`；`GOMEMLIMIT`/`DATABASE_MAX_*`/`REDIS_POOL_*` 对齐 `deploy/domestic/compose.override.yml` 并 recreate 活动色） | 机械(写) | `ops/observability/probe-cn-prod-pool-env.sh`（经 `run-probe.sh --target cn-prod` 投递；默认只读；须授权） |
| ⚠写侧：first-output 抬闸（`APPLY=yes-first-output-timeouts`；数字来自 `first_output_timeout_ssot.py`） | 机械(写) | `ops/observability/remediate-first-output-timeouts.sh` |
| ⚠写侧止血：tokensea Anthropic 受管账号补挂 `claude` 分组（`APPLY=1`；补 `account_groups` + `scheduler_outbox`） | 机械(写) | `ops/observability/remediate-tokensea-claude-group.sh`（经 run-probe 投递；仅当投影未自动入组的历史缺口） |
| ⚠写侧止血：`groups.model_routing` JSON array→object（`APPLY=1`；可选 `CLEANUP_STICKY_PROBES=1` soft-delete `__tk_probe_sticky*`；否则 ListActiveGroups 反序列化失败 → 万能 key 全站 500） | 机械(写) | `ops/observability/remediate-model-routing-array.sh`（经 run-probe 投递） |
| Docker access log 解析（status/model/minute/latency 直方图 + marker 计数） | 机械 | `ops/observability/parse-access-log.py --stdin\|--file\|--docker` |
| live-host 运行态漂移（镜像 tag + first-output env vs SSOT）| 机械 | `ops/stage0/assert-live-host-state.sh`（SSOT=`first_output_timeout_ssot.py`；`--apply`/`--check`=`scripts/checks/first-output-timeout-ssot.py`；deploy-stage0 / daily diagnostics）|
| Gateway "http request completed" 最近 N 行 tail（脱敏 → JSON array，轻量原始日志） | 机械 | `ops/observability/probe-tail-gateway-logs.sh`（经 run-probe 投递；`LIMIT` 默认 50、`SINCE` 默认 24h、可选 `UNTIL` / `REQUEST_IDS` / `PATH_FILTER` / `STATUS_CODE` / `CONTAINER`；钉 `REQUEST_IDS` 时附带库表对账） |
| Dashboard 预聚合覆盖度诊断（"使用趋势只显示 2 天"：usage_dashboard_daily/hourly vs raw usage_logs + aggregation watermark） | 机械 | `ops/observability/probe-dashboard-aggregate-coverage.sh`（经 run-probe 投递；只读 `row_to_json`） |
| Dashboard 网关中转延迟 vs 端到端 duration（`gateway_latency_ms` 是否被 body 泵送污染；对比 usage_logs 与 usage_dashboard_daily 聚合） | 机械 | `ops/observability/probe-gateway-latency-dashboard.sh`（经 run-probe 投递；只读 `row_to_json`） |
| Admin UI access-log 性能画像（/admin 前端资源 + /api/v1/admin/* latency p50/p90/p95 + slow samples） | 机械 | `ops/observability/probe-admin-ui-perf.sh`（经 run-probe 投递；只读 Docker logs 聚合） |
| Admin UI API timing（逐页接口 curl TTFB/total/size/非 2xx，含 dashboard/usage/accounts/ops/payment 等页面形状） | 机械 | `ops/observability/probe-admin-ui-api-timing.sh`（经 run-probe 投递；只读 admin API key + curl，无 mutating endpoints） |
| Admin aggregation runtime config（dashboard aggregation env/config + hourly/daily/model/group marker 覆盖度） | 机械 | `ops/observability/probe-admin-aggregation-config.sh`（经 run-probe 投递；只读 env + SELECT） |
| Admin model rollup timing（dashboard/models 冷态慢：raw 7d group-by vs usage_dashboard_model_daily + raw today 耗时/一致性） | 机械 | `ops/observability/probe-admin-model-rollup-timing.sh`（经 run-probe 投递；只读 SELECT + EXPLAIN ANALYZE） |
| Admin group rollup timing（usage/dashboard group distribution 冷态慢：raw 7d group-by vs usage_dashboard_group_daily + raw today 耗时/一致性） | 机械 | `ops/observability/probe-admin-group-rollup-timing.sh`（经 run-probe 投递；只读 SELECT + EXPLAIN ANALYZE） |
| 图片/视频盯盘（成功计量计费 + 错误分面 + 计费异常 + last-seen；区分 image vs video、空池 429 vs 真上游错误 vs 缺权限 401） | 机械 | `ops/observability/probe-image-video-billing.sh`（窗口盯盘，`WINDOW_MIN`/`CTX_HOURS`）+ `ops/observability/probe-image-video-deepctx.sh`（openai 账号池/报错归属/流量出处一次性深挖），均经 run-probe 投递；只读 `row_to_json` |
| Studio 图片请求审计（Image Studio / BakeOff prompt 是否实际提交、是否同一轮、size/model 是否被前后端改写） | 机械 | `ops/observability/probe-studio-image-request-audit.sh`（经 run-probe 投递；查 `ops_system_logs component=audit.openai_image_request`；按 `WINDOW_MINUTES` / user / api_key / model / `studio_run_id` / `prompt_sha256` / request id 过滤） |
| 用户级盯盘（一组 user_id 的请求 + 错误 + 计量计费 + 图片/视频 breakout + last-seen，单次 SSM 往返，对齐 30min 汇报节奏） | 机械 | `ops/observability/probe-user-billing-watch.sh`（经 run-probe 投递；默认发现当前 `users.status=active AND deleted_at IS NULL` 清单，`USER_IDS` 可选覆盖、`WINDOW_MINUTES` 默认 30；只读 `row_to_json`，复用 probe-image-video-billing.sh 的 image/video 判别谓词） |
| Kiro 响应兼容历史（按日核对指定/全部模型的 Kiro 承接账号、stream 比例、匿名 user id、User-Agent 版本与匹配错误事件） | 机械 | `ops/observability/probe-kiro-response-compat.sh`（经 run-probe 投递；`DAYS` 默认 15、`MODEL` 默认 `claude-opus-4-8` 且 `*` 表示全部、`UA_LIMIT` 默认每日 20、`UA_FILTER` 可选；只读 `usage_logs`/`ops_error_logs`/`accounts`，不读请求或响应 body） |
| Gateway UA/TLS / usage_logs / ops / docker 指纹交叉对比（窄时间窗） | 机械 | `ops/observability/probe-gateway-ua-tls-compare.sh`（通过 run-probe.sh 投递；`WINDOW_MINUTES` 收窄 DB 窗） |
| `ops_error_logs.request_body` 顶层参数形状聚合（若线上 schema 保留 body，则只输出 top-level keys / deprecated sampling key 存在性；若无 body 列则输出 schema-unavailable + 错误样本） | 机械 | `ops/observability/probe-ops-error-request-shape.sh`（经 run-probe 投递；用于确认错误请求是否携带 `temperature` / `top_p` / `top_k` 等字段，不输出 prompt/body 原文） |
| final error 与 QA evidence 覆盖率（request_id 关联、retention、blob ref/本地存在性；不输出正文或 URI） | 机械 | `ops/observability/probe-qa-error-evidence.sh`（经 run-probe 投递；先判断是否有可深挖证据，再决定是否需要隐私受控的正文检查） |
| `SUB2API_DEBUG_GATEWAY_BODY` 日志拉回本机（SSM gzip → S3 presigned PUT → 本地 gunzip） | 机械 | `ops/observability/fetch-gateway-debug-log.sh --target prod`（**本地** orchestrator，不走 run-probe） |
| anthropic capacity / cap 与 schedulable 证据 | 机械 | `ops/observability/probe-caps.sh`（通过 run-probe.sh 投递） |
| Scheduler snapshot 桶 vs DB 对齐（`ListSchedulableAccounts` mixed/single 路径：Redis `sched:*` zcard、DB 可调度池、outbox 尾） | 机械 | `ops/observability/probe-scheduler-snapshot-bucket.sh`（经 run-probe 投递；`GROUP_ID` 必填、`PLATFORM` 默认 gemini、`MODE` 可选 mixed/single/forced） |
| 时间窗规范（UTC ↔ Asia/Shanghai 双写） | 判断 | prompt（含报告口径，无机械抓手） |
| 根因 / 风险分级 / 建议下一步 | 判断 | prompt（爆炸半径 / 回滚成本） |
