---
title: Kiro OAuth monthly recovery and live verification
status: approved-for-implementation
approved_by: user
risk: high
---

# 目标

每月 1 日北京时间 08:00 左右，恢复 prod 与 cn-prod 中全部 Kiro OAuth 账号的本地错误、限流和不可调度状态，随后逐账号发起一次真实 Kiro 文本请求；只有真实请求成功的账号才能继续参与调度。流程必须经 GitHub Actions → AWS OIDC → SSM 执行，保留结构化结果和失败原因。

# 当前证据

- prod 当前有 30 个 Kiro OAuth 账号，cn-prod 当前没有 Kiro 账号。
- 账号恢复使用现有 `POST /api/v1/admin/accounts/:id/recover-state`。
- 真实 Kiro 能力可用现有 `ops/stage0/probe_kiro_upstream_models.sh` 验证；`claude-sonnet-4.5` 是当前账号共同可探测的稳定文本模型。
- 近期实测中 27 个账号 HTTP 200，3 个账号因 Kiro OAuth client secret expired 返回 403；失败账号不能被自动强行放回调度池。

# 做什么

1. 新增一个受显式 guard 保护的 prod/cn-prod SSM 运维脚本，读取当前非删除 Kiro OAuth 账号清单。
2. 对每个账号调用 `recover-state`，清理错误、限流、临时不可调度状态和 OAuth token cache。
3. 对每个账号执行一次真实 Kiro 文本请求，记录账号 ID、模型、HTTP 状态、探测 verdict 和脱敏错误分类。
4. 成功账号保持 `active + schedulable=true`；失败账号保持不可调度，并在结果中区分额度不足、OAuth 失效、传输失败和配置失败。
5. 新增 GitHub Actions 定时入口，使用 AWS OIDC 和现有 `run-probe.sh`，提供 `workflow_dispatch` 的 `dry-run` 与显式 `apply` 入口、并发锁和 artifact 证据。
6. 定时器按 UTC 运行。北京时间 08:00:05 对应 UTC 00:00:05；GitHub Actions schedule 只有分钟粒度，因此在 00:00 UTC 附近触发后由 SSM 脚本等待或立即执行，并在结果中记录实际开始时间与目标时间。

# 不做什么

- 不修改 Kiro OAuth credentials、client secret、model mapping、分组或协议能力 SSOT；这些属于独立的账号导入/协议治理流程。
- 不把 Kiro 账号加入通用 `protocol_endpoint_capabilities`；Kiro 继续走专用 Kiro Gateway。
- 不强行把真实探测失败的账号设为可调度，也不把上游 402/403 伪装成本地恢复成功。
- 不向 cn-prod 创建账号；没有账号时只输出空集成功结果。

# 主要风险与控制

| 风险 | 控制 |
| --- | --- |
| 定时任务重复运行 | GitHub Actions concurrency 锁；远端按北京时间月份写入幂等 marker，已完成月份拒绝重复 apply |
| 大量账号同时请求上游 | SSM 脚本固定并发上限、账号级超时和总超时 |
| 恢复后坏账号进入调度 | 成功探测与失败状态分离；失败账号保持 `schedulable=false` |
| GitHub cron 延迟 | 记录目标/实际时间；支持手动 dispatch 重跑；不把 cron 当作秒级 SLA |
| 凭证或响应泄露 | 远端临时凭证文件 0600；artifact 仅保留结构化脱敏结果 |

# 验证

- 脚本单测覆盖：结构化成功/失败解析、脱敏错误边界、402/403 失败分类、超时/传输/调度恢复失败分类，以及 dry-run/部分失败汇总。
- 运行一次 `dry-run`，确认不改账号状态。
- 在人工批准后运行一次 `apply`，检查账号状态、真实请求结果、artifact 和失败分类。
- 提交前运行 `./scripts/preflight.sh`，并按项目 review/合并门禁执行。

# 需要人工确认的设计点

1. 接受 GitHub Actions 只能按分钟调度，目标定义为北京时间 08:00:05 附近执行，并由脚本记录实际时间。
2. 接受“先恢复、再实测；失败账号保持不可调度”的状态策略。
3. 接受默认探测模型使用 `claude-sonnet-4.5`，后续模型漂移由独立 Kiro fingerprint/model 流程维护。
