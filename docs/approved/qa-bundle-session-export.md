---
title: 基于 QA Bundle 的保真会话导出与 traj SSOT
status: approved
approved_by: "user (2026-09-11 conversation: 同意目标收敛为基于 QA Bundle 的保真会话导出；吸收高优先级功能并收敛 traj SSOT)"
approved_at: 2026-09-11
created: 2026-09-11
authors: [codex]
risk: high
---

# 基于 QA Bundle 的保真会话导出

## 授权与边界

用户已批准会话及工具链重建、推理信息保留、字段来源可追溯，以及 TokenKey 仓库内
traj SSOT 和过时内容清理。本文记录该实现授权；不授权部署、删除线上证据或改变采集权限。
QA 存储、授权、归档与下载生命周期继续由
[QA lifecycle](design-prod-qa-24h-s3-lifecycle.md) 裁决。

## 导出契约

现有 QA 面板的 ZIP 导出从同一个 committed Bundle 生成：

- `qa-records.jsonl`：现有逐请求 Record 和已脱敏证据，仍是可追溯的源数据。
- `sessions.jsonl`：`tk-session/v1` 会话对象；原生消息/item 原样保留，不将摘要替代推理密文，
  不把 signature 当作明文推理，不新增样例兼容格式。
- `export-manifest.json`：Bundle generation、水位、页校验和、导出 schema 和记录/会话计数。

导出版本参与 ZIP job identity，已有 immutable ZIP 不被覆盖，也不会被误复用为新格式。
旧无版本 ZIP job 保持可读；新请求只创建当前版本。更改 projector 必须刷新 Bundle Worker，
沿用既有 worker-surface 发布门禁。发布由另一次明确授权完成。

## 会话与保真

- 会话以 user/API key/客户端 wire shape 为硬边界。稳定 `synth_session_id` 优先；普通
  `trajectory_id` 是逐请求关联，不能作为稳定会话 ID。
- 无稳定 ID 时，只在请求历史包含之前已观测请求及 assistant 输出的精确前缀且匹配唯一时
  关联；支持交错会话和跨 Bundle page 延续。相同提示、重试和短历史不能自行证明同一会话。
  歧义或无法证明的历史变更保留独立会话并标识推断边界。
- 首条记录保留全部请求历史；后续只去除已证实的前缀。稳定 ID 下历史重写则保留新的历史
  快照并标记边界，不静默丢弃工具结果旁边的用户内容。
- 每个 call 和 turn 都关联 `qa-records.jsonl` 的 request_id 及证据 JSON 路径。SSE 的 `source.path` 指向原始 chunks，`projection_path` 指向重建响应中的块，不能伪装成原始 JSON 路径。
  保留每次调用参数、模型、usage、终止状态、网关改写标记、采集状态及脱敏版本。
- SSE 先重建原生响应；Responses 优先采用已完成 item/terminal output，同时保留 summary、
  encrypted_content、id、phase；Anthropic 保留原生块顺序、sig-only 和 redacted_thinking。
  未知字段保留在原生对象或原始证据中，无法完整重建必须标记，不推断不存在的推理明文。
- sidecar `internal_thinking_blocks` / `encrypted_reasoning` 单列来源，不能覆盖 client-facing blocks。
  工具链接保留 ID 和 call/result 源路径；缺失或歧义标记 unresolved，不伪造匹配。
- 不支持的端点、缺失或损坏的证据仍计入会话导出且报告原因；不静默过滤成功/失败调用。
- ZIP 原始记录与会话共用已验证页面；按记录处理，索引和片段使用 8 MiB 计量预算的缓冲，
  超限后转入单个私有临时 SQLite 文件（2 MiB page cache）；历史规范化缓存另限 1 MiB。
  缓冲预算计入条目开销，不等同于进程 RSS 上限；内存仍受单条输入大小影响，不随整个日窗口或长会话增长。失败不发布 ZIP，取消会停止处理并清理临时文件。

## Implementation / Owners

本表是 `traj-ssot` 的唯一 owner 清单；根文件只保留指针。

| 职责 | Owner |
| --- | --- |
| 会话 schema、归组、工具关联、来源 | `backend/internal/observability/trajectory/session.go` |
| 有界缓冲、索引和片段落盘 | `backend/internal/observability/trajectory/session_store.go` |
| 原生响应和 SSE 重建 | `backend/internal/observability/trajectory/session_stream.go` |
| 客户端 wire shape | `backend/internal/observability/trajectory/wire_shape.go` |
| Bundle Record 适配、ZIP/清单生成 | `backend/internal/observability/qa/bundle/session_export.go`、`publisher.go` |
| 不可变 job 与版本 identity | `backend/internal/observability/qa/bundle/job.go` |
| 用户授权、签名与 job 接线 | `backend/internal/observability/qa/service_bundle.go` |
| UI 入口与行为 | `frontend/src/components/keys/QABundlePanel.vue`、`frontend/src/composables/useTkQABundle.ts` |
| 防回流与行为门禁 | `scripts/checks/traj-ssot.py`、`scripts/sentinels/trajectory.json` |

仍在使用的 writer/blob/redaction、QA 采集、`trajectory_id` 以及 `traj_export_enabled` 授权字段
不因名称带 traj 而删除。历史 migration 不改；旧 API 的 tombstone/负向回归保留。
旧无调用的 traj v2 投影和其专用测试移除；能力验收迁到当前 Bundle 会话导出链路。

## 验收

核心行为见 US-055（会话导出）：多轮工具链、交错会话、跨页、相同提示不误并、
稳定 ID 历史重写、sig-only、summary+encrypted、流式/非流式、原生 phase/item id、
sidecar 来源、失败/截断/缺证据、权限拒绝、checksum 拒绝、版本与重试幂等。
真实 UI 由 Playwright 走现有 QA 面板、导出并检查下载的 ZIP 内容。
本地实现和测试不代表已经部署。

## 性能验证

`backend/internal/observability/qa/bundle/session_export_benchmark_test.go` 用同一批 committed
Bundle 对比 raw ZIP 和会话 ZIP，分别报告 CPU、生成耗时、磁盘占用和产物字节；不包含 S3 网络。
运行：在 backend 下执行 `GOMAXPROCS=1 go test -tags=unit ./internal/observability/qa/bundle -run '^$' -bench '^BenchmarkSessionExport$' -benchtime=3x`。
落盘切换的字节等价、固定文件数、取消和重复请求拒绝由 US-055 回归测试覆盖。
