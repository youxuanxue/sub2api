# GitHub Actions 消费关系与减负盘点

审计基线：main `ddb9eb1f433692735ba852f585ce1ac9efbe9e04`（2026-10-18）。
以下是该快照的决策记录，不是持续维护的部署配置清单；现网身份仍以
[舰队入口](../../deploy/hetzner/README.md) 为准。
本 PR 仅交付盘点，不修改 workflow 或禁用远端任务，不宣称新增运行成本节省。

Edge 网关退役不等于 Gemini Web Worker 退役：用户明确确认 prod 将继续使用
Worker，因此保留它的 PR 校验、main push 构建发布和手动入口。

## 有明确消费者，保留

文件均位于 `.github/workflows/`。表中的消费包括运行时产物、门禁和运维动作，
不以“有历史运行”或“上传了 artifact”单独证明价值。

| Workflow | 消费者 / 保留理由 |
| --- | --- |
| `backend-ci.yml` | PR/main 测试门禁；每周安全检查发现随漏洞库变化的新风险，weekly smoke 验证依赖可编译性 |
| `release.yml` | GHCR 发布镜像与版本产物供 prod 部署使用 |
| `deploy-stage0.yml` | prod deploy、replay 和 smoke-only |
| `warm-image-stage0.yml` | prod 部署消费预拉取的镜像层；不是 Edge 专用任务 |
| `warm-release-cache-main.yml` | CI/release 恢复共享 Go cache；是否降低每日预热频率应以命中与构建耗时证据决定 |
| `gemini-web-worker.yml` | prod 继续消费 Worker 镜像；Edge 退役不能作为停发依据 |
| `main-ancestry-guard.yml` | PR ancestry 与 main anchor 检查 |
| `marker-acknowledgement-pr.yml` | PR 元数据中的 marker/覆写确认，单纯本地 preflight 无法替代 |
| `upstream-conflict-surface.yml` | upstream 冲突、平台 registry 与侵入性检查 |
| `upstream-merge-pr-shape.yml` | 仅 upstream merge PR 的结构与 sentinel 契约；条件 skipped 不代表无价值 |
| `upstream-issue-watchdog.yml` | 上游 issue 扫描与显式修复模式，供缺陷跟踪消费 |
| `upstream-merge-notify.yml` | 上游提交漂移提醒；与 issue watchdog 的问题来源不同 |
| `pricing-registry-sensor.yml` | 价格变化生成审阅候选，不能替代发布动作 |
| `pricing-registry-publish.yml` | 已合并价格配置的 runtime 发布，消费 sensor 经审阅后的变更 |
| `client-fidelity-watch.yml` | 客户端版本、registry、prod 指纹与 Kiro parity 形成报告/issue 闭环；下节列出可合并的纯摘要 job |
| `ops-daily-diagnostics.yml` | prod 诊断、issue、修复候选与手工日志下载；消费备份 canary 回执 |
| `ops-payment-billing-watch.yml` | 支付/计费告警与周报，不因 Edge 退役降低异常检测频率 |
| `ops-pgdump-restore-canary.yml` | 实际验证备份可恢复，与仅生成 dump 的任务不同 |
| `ops-stage0-pg-dump-refresh.yml` | 手动刷新 prod pg_dump 宿主任务 |
| `ops-stage0-host-mem-guard.yml` | 手动修复 prod 宿主内存配置 |
| `ops-stage0-container-log-policy.yml` | 手动同步容器日志策略 |
| `ops-stage0-ghcr-prune-timer.yml` | 手动刷新 prod 镜像清理 timer；已没有 Edge matrix |
| `prod-feishu-config-sync.yml` | prod 告警配置自愈，替代旧 fleet 同步；不是远端孤儿 |
| `ops-repair-draft.yml` | 消费 daily diagnostics 的脱敏产物生成修复 Draft PR |
| `pr-repair-agent.yml` | 显式指定 PR 的修复入口，与 ops 诊断修复的输入不同 |
| `sync-docs-to-pages.yml` | 手动将选定文档同步到自定义页面存储；公开 quickstart 不依赖它 |

## 减负候选与证据边界

| 优先项 | 证据 | 建议 |
| --- | --- | --- |
| `model-release-watch.yml` 日检报告消费不闭合 | schedule 不执行 issue sync；只有手动 `open_issues=true` 才同步。日检保存 novelty state 和三天 artifact，未在仓库找到自动下载该报告的下游；本地 `model-release-watch.sh issues` 是人工消费入口 | 不再标为“已确认有消费”。先确认报告使用方；无人使用时可暂停 schedule，有需求时应接通现有 issue 消费而非只持续产报。不能据仓库搜索断言没有外部人工读者 |
| `client-fidelity-watch.yml` 的 `contract` job | 在 daily-report 之后仅 echo 结果，无断言、无发布输出；真正 verdict 已由 daily-report enforce | 将摘要并入 daily-report，保留 verdict 与故障状态；实施前确认远端 required checks 未引用该 job 名 |
| `cla.yml` | job 条件仅允许 `Wei-Shaw/sub2api`；本 fork 不执行 CLA 动作 | 可删除 fork 的无效入口，收益是减少 skipped 运行噪声，不是大量 runner 分钟；上游同步时避免恢复 |
| `deploy-prod-hetzner-stage0.yml` | 历史 Hcloud 建机模板，当前 prod 是 Tokyo；没有 schedule，但修改共享 pgdump/prune 脚本仍触发 PR 校验 | 将仍有价值的 bootstrap 校验纳入现有 CI 后可退役部署入口；不能直接删掉唯一验证路径 |

这些候选不是本 PR 已执行的清理。没有 runner 计费和 artifact 下载统计，
不估算月度费用，也不将人工入口低调用量等同于零价值。

## 已由基线完成的 Edge 清理

下列文件在审计基线已经删除，不再作为待实施事项：
`deploy-edge-hetzner-stage0.yml`、`deploy-edge-lightsail-stage0.yml`、
`deploy-gemini-web-worker.yml`、`edge-health-watch.yml`、`fleet-feishu-config-sync.yml`。

`client-fidelity-watch.yml` 的 Edge OAuth job、GHCR prune 的 Edge matrix
以及 restore canary 的 Edge selector 也已移除。恢复 Edge 架构与 prod Worker
的使用需求是不同决策，不应混在一项清理里。

## 指定运行的核对

[39091340351](https://github.com/youxuanxue/sub2api/actions/runs/39091340351)
是 **Stage0 Warm Image**，在 prod 预拉镜像，后续部署复用层缓存。
它没有切换运行容器，但会写入主机镜像缓存，不是严格的只读操作。
保留它的理由是 prod 部署消费关系；单次运行成功不能证明预热总耗时一定更短。
