---
title: US Edge 8 GiB snapshot migration
status: approved
approved_by: feng
approved_at: 2026-10-09
authors: [agent]
created: 2026-10-09
---

# US Edge 8 GiB 快照迁移

本次会话授权原文：「先保持 prod 不变，edge 升到 2 核 8G 过渡」「四台 US Edge（每月增加约 $128）」「需要保持数据不丢、用户体验尽可能无感知」。本文件记录这项既有运维授权；不代表已批准合并代码或扩大 IAM 权限。

## 约束与执行

us3/us4/us5/us6 逐台摘流、等待 HTTP 与异步生图结束、停应用写入、验证 S3 数据库备份的真实恢复，再通过完整冷磁盘快照迁移。普通逻辑备份排除的日志必须由完整快照保留。新机开启写入前比对冷文件 SHA-256 与核心表数量，保持原静态 IP、应用镜像和凭据。原机与快照保留。新机产生写入后禁止回切陈旧磁盘。

## 新机注册准备

普通 operator IAM user 没有 iam:PassRole；复用现有 Edge workflow 的 OIDC 角色，通过 `prepare-resize` 准备新机单次注册身份。该步骤只是既有迁移授权的必要管理准备，不创建/停止实例，不切 IP、调度或活跃 SSM 指针，也不修改 IAM。

目标、region、Hybrid role 和参数前缀只从现有矩阵解析；要求确认源实例名、新实例名不同且新实例尚不存在。权限或网络错误不当作资源不存在。注册凭据限制一次、四小时过期，写入独立 workflow run/attempt 的 SecureString，不覆盖已有凭据，不输出 activation code。交接失败撤销本次新建 activation。完成注册后清理该临时凭据。

状态：目标确认 → 单次 activation → 加密参数交接 → operator 验证边界并创建冷快照新机 → 新机注册验证 → 删除临时参数。生成身份失败时，原服务和所有业务数据不受此步骤影响。

## 验证

聚焦测试覆盖成功、既存目标、未知网络/权限状态、错目标、非法 run 标识与加密交接失败撤销。正式运维验收和容量证据见 `docs/ops/edge-image-capacity-upgrade-20261009.md`。无 Web/UI 工件；不以 API 检查冒充 UI e2e。
