---
title: 为已创建的 Lightsail 迁移新机补齐 SSM 身份
status: pending
approved_by: pending
created: 2026-10-15
owners: [tk-platform]
scope: ops/lightsail/prepare_resize_identity.py + AWS Standby Edge Lightsail Deploy
---

# 已创建新机的管理身份

用户已授权迁回 AWS，保留 prod、uk1、uk2、us4、us5 架构，prod 也独立新建；
采用直接创建新机、从现网 Hcloud 恢复、基础服务验收，不进行负载压测。
已创建的 canary 不能使用仅接受“新机尚不存在”的身份准备入口。
本变更只补齐既有入口的身份校验分支，待合并确认；不改变迁移切流授权或现网目标矩阵。

## 行为与边界

现有 `prepare-resize` 增加可选 `replacement_arn`。未提供时保持拒绝已有实例；
提供时必须匹配真实新机 ARN、region、running 状态及 README 定义的迁移名称和标签。
已注册或尚未过期的同名 activation、同名 SSM managed host 均拒绝重复申请。
生成的身份仍使用对应 edge 的现有 IAM role，单次注册、四小时过期，交接到
每个 run 独立的 SecureString；包含 ARN 供 SSH 注册前复核，不输出 activation code。
交接失败只撤销本次新 activation。

不修改 IAM、本地凭据权限、实例、DNS、IP、业务数据、SSM 活跃指针。
注册完成后仍须验证新机 SSM 命令可达，才能撤销临时 SSH 白名单；
迁移源停止写入、最终数据同步、切 DNS 和开启新机上游访问属于后续切流步骤。

## 验收

`ops/lightsail/test_prepare_resize_identity.py` 覆盖新建前身份准备、已有 canary 正向、
缺少确认/错误身份/不确定 AWS 查询的拒绝、重复身份拒绝及交接失败回收。
真实注册在合并后通过既有 OIDC workflow 执行；上线前继续验证备份、监控和基础服务。
