# GitHub Actions workflow inventory

这份清单基于本任务分叉点 `acb0e0425f` 的 workflow，远端 main 已继续演进（见末节）。
2026-10-17 起网关为 Tokyo prod，Edge 身份的
`deployable` 集合为空；历史代码、脚本和恢复入口仍可能被测试或审批基线引用。
判断一个 workflow 是否“有后续消费”时，同时检查触发器、下游 job、现役主机和
产物消费者，不能只看文件名。

## 已确认保留

| Workflow | 现有消费 | 结论 |
| --- | --- | --- |
| `backend-ci.yml` | PR/main 门禁、每周安全和 smoke 任务 | 保留 |
| `cla.yml` | job 仅允许 `Wei-Shaw/sub2api`，本 fork 的运行均 skipped | 清理候选；在 fork 无实际消费者 |
| `deploy-stage0.yml` | Tokyo prod deploy、replay、smoke-only | 保留 |
| `warm-image-stage0.yml` | 在 prod deploy 前预拉镜像；随后由 `deploy-stage0.yml` 消费 | 保留 |
| `warm-release-cache-main.yml` | main push 后为 CI/release 预热 Go cache | 保留 |
| `release.yml` | GHCR 镜像、版本和 release 产物 | 保留 |
| `main-ancestry-guard.yml`、`marker-acknowledgement-pr.yml` | main/PR 纪律门禁 | 保留 |
| `upstream-conflict-surface.yml`、`upstream-merge-pr-shape.yml` | upstream merge PR 的冲突和结构门禁 | 保留 |
| `upstream-issue-watchdog.yml`、`upstream-merge-notify.yml` | upstream issue/drift 发现与提醒 | 保留 |
| `model-release-watch.yml`、`pricing-registry-sensor.yml`、`pricing-registry-publish.yml` | 模型/价格目录发现、审阅和发布 | 保留 |
| `client-fidelity-watch.yml` | prod 指纹、registry、Kiro parity 和 issue 追踪 | 保留；见下方 Edge 子任务 |
| `gemini-web-worker.yml` | prod 继续消费 Gemini Web Worker 镜像；PR/main push 校验与构建发布 | 保留 |
| `ops-daily-diagnostics.yml`、`ops-payment-billing-watch.yml` | prod 日常诊断、计费/支付告警 | 保留 |
| `ops-pgdump-restore-canary.yml`、`ops-stage0-pg-dump-refresh.yml` | prod 备份恢复演练与 pg_dump 刷新 | 保留；Edge selector 是死分支候选 |
| `ops-stage0-host-mem-guard.yml`、`ops-stage0-container-log-policy.yml` | prod 宿主机运行时修复 | 保留 |
| `fleet-feishu-config-sync.yml` | prod 告警配置自愈；Edge 为空时只剩 prod | 保留，后续可删除 Edge fan-out 分支 |
| `ops-repair-draft.yml`、`pr-repair-agent.yml` | 诊断产物到 draft PR 的人工修复闭环 | 保留 |
| `sync-docs-to-pages.yml` | 手工文档发布 | 保留 |

## 已减负或已停止自动消费

| Workflow/部分 | 证据 | 处理 |
| --- | --- | --- |
| `edge-health-watch.yml` 的定时触发 | 当前文件只有 `workflow_dispatch`，空 Edge 集合会直接退出；prod 监控由宿主 watcher 和 daily diagnostics 承担 | 保持手动诊断，不产生定时 Actions 消耗 |
| `client-fidelity-watch.yml` 的 `edge-oauth-mimic-aggregate` | Edge 集合为空，当前运行只做空扫描；prod/registry/Kiro 仍有消费 | 作为下一步低风险清理候选，删除前需同步 report 聚合参数和 issue 工具 |
| `ops-stage0-ghcr-prune-timer.yml` 的 `all-deployable-edges` | resolver 当前返回空集合；`prod` 分支仍用于现役主机 | 保留 prod，后续删除 Edge matrix job |

## 退役恢复入口（当前不产生定时负担）

以下 workflow 都没有 schedule，且入口已经要求 `I_APPROVE_EDGE_RESTORE` 或由
退役矩阵硬拒绝。它们仍被 preflight、sentinel、迁移测试或历史恢复文档引用，
所以本次不直接删除文件，避免留下未同步的死引用：

- `deploy-edge-hetzner-stage0.yml`
- `deploy-edge-lightsail-stage0.yml`
- `deploy-gemini-web-worker.yml`
- `deploy-prod-hetzner-stage0.yml`（迁移/重建模板）

若产品决策确认永久不恢复 Edge，应在同一个变更中一并移除这些 workflow、
`workflow-edge-coverage.json`、对应 preflight/sentinel/tests 和恢复文档；只删
YAML 会把 CI 变成“文件不存在”而不是减负。

## 关于 `39091340351`

该 run 的 workflow 是 **Stage0 Warm Image**。它在 prod 主机上执行只读的
`docker pull`，紧接着的 `Stage0 Deploy` 会消费已缓存的镜像层；因此它不属于
Edge 体系，也不应因 Edge 退役而删除。

## 与远端 main 的差异

本工作树落后于远端 main，不能将本地缺失误判为远端孤儿。
远端已提供 `prod-feishu-config-sync.yml`，替代旧 fleet 同步入口；应保留它，
不应因曾显示零次运行而禁用。远端 workflow 清单也已不再登记本地的
`deploy-edge-hetzner-stage0.yml`、`deploy-edge-lightsail-stage0.yml`、
`deploy-gemini-web-worker.yml` 和 `edge-health-watch.yml`。
上面的 Edge 清理候选须先与最新 main 核对，避免重复实施已完成的退役。

用户明确确认 prod 将继续使用 Gemini Web Worker，因此构建发布 workflow
必须保留。Edge 部署入口的退役不等于 Worker 产品能力退役。本 PR 仅交付盘点，
不修改 workflow、不禁用远端任务，也不宣称已经实现新的运行成本节省。
