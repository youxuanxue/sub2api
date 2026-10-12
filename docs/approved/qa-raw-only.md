---
title: QA raw-only 归档与 Hetzner 迁移
status: approved
approved_by: "user（本会话：砍掉 QA 主机、QA bundle 及相关产品功能，只保留定时 prod QA 归档到 raw S3）"
owners: [tk-platform]
risk: high
---

# QA raw-only

用户决定删除 QA 主机、Bundle/ZIP 和用户导出能力。本文替代 QA Bundle 会话导出及 uk1 共享 worker 的设计决策；网关蓝绿发布继续沿用既有 owner。

## 保留链路和 owner

prod gateway capture → capture ledger / qa_records / local blobs → tokenkey-qa-maintenance.timer → raw S3。

| 职责 | 唯一 owner |
| --- | --- |
| 捕获与脱敏 | backend/internal/observability/qa/service.go、sse_tee.go |
| raw archive 与校验 | backend/internal/observability/qa/archive/ |
| 归档、恢复验证后清理 | backend/cmd/server/qa_maintenance.go、qa/lifecycle/ |
| 定时隔离执行、资源限制 | deploy/aws/stage0/tokenkey-qa-maintenance.sh |
| 策略 | ops/qa/policy.yaml |
| 运维 restore | backend/cmd/qa-archive/ |

raw 不开放给产品用户；保留已有 capture seal、raw commit、restore_verified 和 DROP pause 等防丢失门禁。Edge capture 继续关闭。

## 删除契约

移除 /users/me/qa/bundles、bundle-exports 及旧 traj export；退役端点返回 404。删除 API Key 页面 QA 入口、管理员导出授权控件、traj_export_enabled / traj_export_platforms API 字段、Bundle 服务、worker、队列、canary 和发布 workflow。数据库旧授权列和历史迁移保留作兼容数据，不执行 DROP COLUMN。

## 存储和迁移

raw 目标为私有 Hetzner Object Storage，region fsn1、HTTPS、固定 raw/v1 prefix；使用用户已授权的现有项目 S3 凭据，经受保护文件挂载，禁止打印或提交凭据。不增加 SSE-C、KMS 或应用加密。

先完成离线预复制，再在暂停 maintenance 写入与 DROP 的窗口完成最终增量和逐对象 SHA256 对账；capture 持续运行。确认所有 committed shard、manifest、segment 和 control evidence 可读取、恢复验证通过后，切换独立 maintenance pin 的存储坐标。普通 gateway deploy 不得替代迁移步骤，也不得静默切换 raw bucket 或 endpoint。

若复制、对账、restore 或健康门禁失败，保持 DROP pause 和 AWS 源，恢复原 maintenance 配置；不删除源数据。冻结期间须监控磁盘和捕获队列，超出既有容量门禁则回退并恢复定时归档。切换后验证连续定时归档、restore、生命周期健康和 prod/edges 网关健康，观察期后再单独审批销毁 AWS 数据与资源。

## 线上安全边界

代码 PR 不触发部署。uk1 QA 容器和 Bundle AWS 资源在代码合并、切换验证后按资源名核实并停用；任何销毁单独记录证据。禁止操作 tokenkey-blue/green、Caddy、主 PostgreSQL/Redis，禁止重启宿主机。主网关异常时停止迁移并回退 QA 操作。

## 验证

运行 capture/archive/lifecycle 既有测试、退役 API/UI 行为测试、raw 存储坐标和凭据隔离测试、maintenance 容器限制测试、迁移重试/对账测试、preflight 和 review。UI 验收经真实 Playwright 浏览器。线上迁移与资源退役证据在执行后补充；本文不宣称已上线。

## 运维工具接口

以下命令仅描述后续迁移接口，本 PR 不执行线上命令。`--apply` 只复制和校验目标对象；不改变 producer、不删除源。命令须在已加载受保护环境的终端运行，禁止 shell trace 和输出凭据。

```bash
python3 ops/qa/migrate_hcloud_objects.py --kind raw --ledger "$QA_MIGRATION_LEDGER"
python3 ops/qa/migrate_hcloud_objects.py --kind raw --ledger "$QA_MIGRATION_LEDGER" --apply --mib-per-second 10
# maintenance 写入与 DROP 暂停后冻结；capture 保持运行。
python3 ops/qa/migrate_hcloud_objects.py --kind raw --ledger "$QA_MIGRATION_LEDGER" --freeze-inventory "$QA_FROZEN_INVENTORY"
python3 ops/qa/migrate_hcloud_objects.py --kind raw --ledger "$QA_MIGRATION_LEDGER" --verify-only "$QA_FROZEN_INVENTORY" --receipt "$QA_VERIFICATION_RECEIPT"
```

清单与回执独占创建、权限 0600。最终验证重新读取所有源/目标对象，校验 SHA256、内容头、元数据和原到期标签；验证前后重新列举源并读取目标 bucket 生命周期配置，拒绝对象漂移、原绝对到期规则缺失/停用/变化及 runtime 保留规则漂移。回执始终 `cutover_ready: false`，还需 raw restore 与独立 maintenance 接受验证。

raw 凭据只挂载给 maintenance 子容器；网关两种颜色不挂载 raw 文件。网关发布只接受 `raw_archive_v1` 目标；缺失、旧 Bundle 和未知契约在触碰主机前拒绝。历史版本回滚须另行验证 raw capture/maintenance 兼容性与 DROP pause，不能由普通发布绕过。

测试入口：[迁移](../../ops/qa/test_migrate_hcloud_objects.py)、[凭据隔离](../../ops/stage0/test_bluegreen_host_runtime.py)、[退役路由](../../backend/internal/server/routes/qa_retired_routes_test.go)、[真实 UI](../../frontend/e2e/qa-retirement.e2e.ts)。
