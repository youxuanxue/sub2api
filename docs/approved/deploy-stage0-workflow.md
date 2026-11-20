---
title: Cloud-Agent-Driven Stage0 Blue/Green Deployment Workflow
status: approved
approved_by: "feng (2026-08-28 revision); youxuanxue (PR #53 initial approval)"
approved_at: 2026-08-28
owners: [tk-platform]
revised: 2026-11-20（A14 退役后删除蓝绿执行百科；仅留 tombstone 与应急入口）
scope: ".github/workflows/deploy-stage0.yml (tombstone) + ops/stage0/deploy_via_ssm_bluegreen.sh (emergency)"
---

# Stage0 蓝绿发布（已退役默认路径）

> **现网默认发版**：`prod-rolling-deploy.yml` / `cn-prod-rolling-deploy.yml`
> （[`design-prod-entry-multi-app`](design-prod-entry-multi-app.md)、
> [`design-cn-prod-rolling-deploy`](design-cn-prod-rolling-deploy.md)）。

## 现状（A14）

- `.github/workflows/deploy-stage0.yml` 为结构 tombstone：GHA job 在主机变更前拒绝执行。
- `dispatch-prod-deploy.sh` 拒绝 `legacy-deploy|replay|smoke-only`。
- **应急**同机蓝绿：`ops/stage0/deploy_via_ssm_bluegreen.sh` + `STAGE0_ALLOW_LEGACY_BLUEGREEN=1`。
- QA：[`design-tencent-cos-backup-node1-qa.md#node1-qa`](design-tencent-cos-backup-node1-qa.md#node1-qa)。

历史 OIDC/切色逐步说明已删除；勿按本文恢复默认蓝绿发版。

## 5. Smoke / recovery pointer

应急路径与 smoke 配置见 [`deploy/aws/README.md`](../../deploy/aws/README.md) 与
[`deploy/aws/RUNBOOK-disaster-recovery.md`](../../deploy/aws/RUNBOOK-disaster-recovery.md)。
