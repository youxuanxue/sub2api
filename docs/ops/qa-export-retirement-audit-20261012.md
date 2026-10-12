# QA 导出退役核验（2026-10-12）

结论：v1.8.287 已退役产品导出，但线上基础设施尚未退役完成。
本次只读核验，无停容器、修改服务、切换存储或删除数据。
批准边界见 [QA raw-only](../approved/qa-raw-only.md)。

## 产品与代码

- v1.8.287 包含 #2599；旧 Bundle、ZIP、traj 导出路由线上均为真实 404。
- Bundle API/UI、worker/queue 实现与发布 workflow 已删除。历史数据库列、
  `qa_export_jobs` 表及 ent 映射保留兼容，不再接入业务；不做 DROP。
- 本次补清 Makefile 已删除测试引用、发布技能、部署说明、root 导航与隐私文案，
  并扩展 `scripts/checks/qa-lifecycle-ssot.py` 防止这些入口重新引入导出功能。
- 线上 `/privacy` 仍展示旧查看/导出承诺；仓库文案修正尚未发布。
- 旧 approved Bundle 文档带 archived/替代说明，保留作历史，不是执行入口。

## 线上待退役资源

| 资源 | 只读观察 | 后续动作 |
| --- | --- | --- |
| uk1 `tokenkey-qa-worker` | running，compose project `tokenkey-qa`，network `tokenkey-qa` | 独立停用后验收；保留状态和数据再决定删除 |
| uk1 `tokenkey-qa-queue` | running/healthy，独立 PostgreSQL | 确认无未完成任务后停用；不得操作主 PostgreSQL |
| uk1 `/var/lib/tokenkey-qa/` | 配置、queue 数据、scratch、独立 secrets 挂载仍被使用 | 停用验证后分别处理；禁止输出 secrets |
| ECS `tokenkey-prod-qa-bundle/tokenkey-prod-qa-bundle-worker` | desired=1，running=1，task definition revision 18 | 独立停用并验证无继续拉起 |
| SQS `tokenkey-prod-qa-bundle`、`tokenkey-prod-qa-bundle-dlq` | 可见和处理中消息均为 0 | 停用消费者并复核后处理 |
| AWS `tokenkey-prod-qa-bundles-682751977094` | 仍有历史对象 | 观察期与单独数据删除审批后处理 |
| AWS `tokenkey-prod-qa-exports-682751977094` | 当前空桶，CFN physical-resource 查询未定位到所属 stack | 确认所有权和依赖后处理 |
| Hetzner `tokenkey-qa-bundles-fsn1-dd2ca9019b22` | 仍有对象，uk1 worker 仍指向该桶 | 停用消费者、核对数据后处理 |
| Bundle IAM task/execution role、security group、log group、task definition | 仍属 `tokenkey-prod-qa-raw-archive` stack | 随独立退役计划处理，不能整栈删除 |
| IAM `tokenkey-gha-us-east-1-qa-infra-deploy`、`tokenkey-us-east-1-qa-infra-cloudformation` | 仍存在 | 核对 raw infra 工具与信任策略后判断是否可删 |

GitHub 未发现 QA/Bundle 专用 repository variables、secrets 或可运行的 QA workflow。
此结论不覆盖 GitHub Environment secrets、AWS Parameter Store、已签发凭据和备份。

## 仍在使用的 raw 链路

prod 活动 gateway 为 1.8.287；独立 `tokenkey-qa-runtime` 固定为 1.8.283，
由 maintenance/boundary timer 执行。最近正常归档为 committed、restore_verified，
缺失 blob 为 0。raw 当前仍写入 AWS
`tokenkey-prod-qa-raw-archive-682751977094`；Hetzner raw 桶预复制不等于切换完成。

**不得删除 `tokenkey-prod-qa-raw-archive` 整个 CloudFormation stack**：
它同时拥有正在使用的 raw 归档和旧 Bundle 资源。迁移、消费者停用与数据销毁
均遵循 raw-only 契约的独立验收边界；不能以本清单替代对账、restore、
maintenance 坐标切换或观察期。清理时保留 raw bucket、恢复角色及归档证据。

## 验证边界

完整 preflight 与 `make test` 通过，覆盖后端单测/Go lint、前端
lint/typecheck 和 547 项关键单测；退役门禁、变异回归及 HTTP route extractor
契约测试通过。先前的本机 Go 缓存边界阻塞已解除。登录态真实 UI 本轮未验证。
本报告是时间点证据，执行停用/删除前必须重新核对资源与网关健康。
