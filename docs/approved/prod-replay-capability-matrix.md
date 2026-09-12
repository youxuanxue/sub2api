---
title: Gateway capability verification independent of deployment
status: draft
risk: high
---

本会话用户已同意能力验证与部署解耦；代码合并、真实上游执行、切流仍是不同动作。

## 边界与 owners

网关正常请求链不承担部署回放的采集要求。移除仅为 replay 新增的
`RequestPath/original_path` 采集，原有 QA lifecycle、截断与脱敏策略继续由 QA owner 负责。
历史回放是可选隔离实验；历史 body、用户、key 配额和 QA 归档可用性不再定义网关能力分母。
旧回执保留原 verdict，不回填或改写。普通 staged promote 校验 prepared candidate 的审批；
仅显式提供 `APPROVED_REPLAY` 时才校验历史实验回执。任何 coverage 报告均不能授权切流。

| Owner | 职责 |
|---|---|
| `backend/cmd/gateway-capability-catalog` | 离线调用现有 `PricingCatalogService.BuildPublicCatalog` 导出模型、模态与能力；无数据库和网关启动 |
| `ops/stage0/gateway-capability-matrix.json` | 验证形态、请求模板和抽样策略；不改变实际模型路由政策 |
| `ops/stage0/fixtures/gateway/` | 短合成请求，保留 tool/thinking/stream/vision 等语义 |
| `ops/stage0/gateway_capability_matrix.py` | 展开、去重、digest、增量选择、预算、结果派生 |
| `ops/stage0/gateway_capability_check.py` | 复用隔离 Sandbox 与 HTTP transport；校验协议响应、终止、usage 和 tool call |
| `ops/stage0/post_release_replay_check.py` | plan/report/run 单一 CLI；run 显式启用上游配额 |
| `scripts/stage0/check-gateway-capabilities.sh` | 无付费调用的 post-release 计划与缺口报告 |

## 有限集合和成本

每个 catalog 模型保留 direct/universal 基础义务。其他协议和工具、思考、多模态、
流式形态按 vendor、模态、能力标签选稳定代表模型；这只是成本抽样，不证明同类其余模型
逐个实测通过，也不替代 `protocolrouter.Plan` 的合法性裁决。新模型自动进入基础集合，
能力/fixture 变化改变 case digest。`--previous` 输入上一版计划，输出新增/变更义务；
没有上一版则诚实地输出完整计划。默认真实执行上限为 32，未选条目仍是未测试。

声明、fixture 存在、harness 通过和真实网关通过分别记录。只有匹配当前 plan/case digest
的 isolated_gateway 实测结果才派生 passed；不允许在 manifest 手写 tested。
图片模型参数、语音 voice、视频异步轮询、转录 multipart 尚未通用化的行明确记录
`blocked-by-test-infrastructure`，不能降为产品不支持。

## 本次旧缺口的处理

| 旧聚合原因 | 补充方式 |
|---|---|
| body_missing_or_redacted（40） | 使用短合成模板，不扩大 QA capture 或修改脱敏 |
| capture_endpoint_ambiguous（4） | 显式 count_tokens/input_tokens/Gemini action 模板，不猜历史路径 |
| capability_not_declared（2） | 从当前 catalog 模态生成义务；媒体缺执行器保持可见 |
| unsupported_path（3） | 用受支持模板路径；历史路径未逐项还原，不声称已定位为某特定协议 |
| historical_response_error（1） | 错误/SSE 异常进入离线验证器负例，不使用失败请求作成功基线 |
| key_quota_exhausted（4） | 专用测试 key ID 绑定；校验 snapshot 中真实 routing_mode，缺失/耗尽报设施缺口 |

这里是旧聚合的处理映射，不是虚构的逐条 54 行审计。媒体类别不能从旧计数推断。

## 使用与验收

离线：`bash scripts/stage0/check-gateway-capabilities.sh /tmp/gateway-check [previous-plan.json]`。
post-release 使用目标 tag 的生成快照与 fixture，并从上一线上 tag 还原 baseline；旧 tag 尚无该检查时输出 baseline_available=false，建立完整基线。自动上传 plan/coverage artifact，不自动执行真实请求，不参与部署判定。

模型声明变更后运行 `python3 scripts/stage0/update-capability-catalog.py`；preflight 的 `--check` 通过 Go owner 防止生成快照漂移。

真实执行只在 prod host 上对已 prepared 的镜像进行：

```sh
python3 ops/stage0/post_release_replay_check.py run \
  --plan /private/plan.json --tag VERSION \
  --bindings /private/probe-key-ids.json --limit 32 \
  --allow-upstream-quota --out /private/results.json
python3 ops/stage0/post_release_replay_check.py report \
  --plan /private/plan.json --results /private/results.json --tag VERSION \
  --out /private/coverage.json
```

bindings 形状是 `{"model-id":{"direct":123,"universal":456}}`，也支持显式 `*` fallback。
只接收保留命名 `__tk_probe_` 且类型匹配的 key ID，secret 只在 snapshot 内取用；
不换用户 key、不重置配额、不在生产创建测试资源。现有 probe key 默认 direct，
universal 专用 key 未准备时必须报缺口。执行采用部署锁、loopback、隔离 DB/Redis、
资源/时间限制及 finally 清理，核对 active 和 prepared 指纹；coverage 输出不写 replay 回执。
本次提交只验证离线行为，不声称已经完成真实能力全覆盖。
