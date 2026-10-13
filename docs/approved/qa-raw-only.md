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

用户追加确认存量 raw 不搬迁：AWS 原归档按原有 7 天生命周期自然到期，保留其原始到期规则与运维恢复入口；停止预复制，不开临时搬迁机，也不在 Edge 上运行搬迁服务。S3 到期删除是异步操作，不以切换后第 7 天必然清空作为验收条件；根据 AWS 最后写入对象的实际到期边界及只读清单确认自然退场。

仅切换新增归档的存储坐标。以 UTC 归档小时记录最后一个 AWS 小时和第一个 Hetzner 小时，先确认旧小时已 committed、restore_verified，且没有未完成 segment 或未覆盖的源记录；不能把旧小时的补写任务误送到缺少原 commit 的新桶。短暂停止并排空 maintenance 与 boundary 两个 owner，保护 DROP，保留原 pin、配置和定时器状态，再切换独立 maintenance pin；capture 持续运行。普通 gateway deploy 不得替代这些步骤，也不得静默切换 raw bucket 或 endpoint。

切换前确认 Hetzner 私有桶的 7 天 raw 生命周期、受保护凭据和现有 S3 兼容性验证。切换后至少观察两次定时新增归档 committed、restore_verified、missing blobs=0，并对新产生的 Hetzner 归档做隔离 restore，核对生命周期及 prod/edges 健康。失败时保护 DROP，按真实小时归属保留两侧数据与恢复坐标，再恢复原 maintenance 配置；不能把已在 Hetzner 提交的小时当作 AWS 已保存。暂停期间监控磁盘、捕获队列与未来分区覆盖，超出既有容量门禁则回退。AWS 源不主动删除；AWS 桶、KMS 和恢复角色等资源销毁仍须另行审批。

切换前保存现有 unresolved capture failure 的精确基线；用户已接受保留历史 `runtime_discontinuity` 警告，不清除 failure、不补造 seal，也不宣称不确定区间已完整捕获或全局 capture hard-health 已恢复。旧警告不授予缺少 seal 的小时 DROP 权限。新增 failure、当前 runtime 不可 seal、捕获积压不收敛、新完成小时缺少有效 seal 或新归档无法恢复，继续阻止切换和验收。

## 线上安全边界

QA 基础设施部署权限由 `deploy/aws/cloudformation/cicd-oidc.yaml` 管理，收敛到 raw archive/audit；删除旧 Bundle、ECS 和 SQS 授权。IAM 栈收尾仅修改 QA 部署角色，不替换角色、不修改其他资源，保留现有 raw 栈的服务角色绑定。

代码 PR 不触发部署。uk1 QA 容器和 Bundle AWS 资源在代码合并、切换验证后按资源名核实并停用；任何销毁单独记录证据。禁止操作 tokenkey-blue/green、Caddy、主 PostgreSQL/Redis，禁止重启宿主机。主网关异常时停止迁移并回退 QA 操作。

## 验证

运行 capture/archive/lifecycle 既有测试、退役 API/UI 行为测试、raw 存储坐标和凭据隔离测试、maintenance 容器限制测试、preflight 和 review。UI 验收经真实 Playwright 浏览器。线上切换与资源退役证据在执行后补充；本文不宣称已上线。

## 运维工具接口

存量预复制与冻结清单工具已随上述决策移除。维护 pin 安装与回退复用 `ops/stage0/qa-runtime-install.py` 和 `deploy/aws/stage0/qa-runtime-transaction.sh`；DROP pause、pin 绑定及验收记录复用 `ops/stage0/prod_release_state.py`；运维恢复沿用 `backend/cmd/qa-archive/`。过渡期间根据已记录的 UTC 小时边界明确选择 AWS 或 Hetzner 恢复坐标，不将单一存储配置误认为包含两侧全部历史。

raw 凭据只挂载给 maintenance 子容器；网关两种颜色不挂载 raw 文件。网关发布只接受 `raw_archive_v1` 目标；缺失、旧 Bundle 和未知契约在触碰主机前拒绝。历史版本回滚须另行验证 raw capture/maintenance 兼容性与 DROP pause，不能由普通发布绕过。

测试入口：[凭据隔离](../../ops/stage0/test_bluegreen_host_runtime.py)、[组件发布](../../ops/stage0/test_prod_component_release.py)、[退役路由](../../backend/internal/server/routes/qa_retired_routes_test.go)、[真实 UI](../../frontend/e2e/qa-retirement.e2e.ts)。
