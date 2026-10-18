---
title: Hetzner host-only PostgreSQL backup storage
status: approved
approved_by: "feng (同意，请继续：主机告警更新、备份迁移及媒体遗留配置清理)"
approved_at: 2026-10-13
risk: high
---

# Hetzner PostgreSQL 备份存储

## 目标与批准范围

prod 与活跃 Edge 的定时数据库备份写入专用私有 Hetzner Object Storage 桶。
沿用现有 pgdump、恢复 canary、两小时周期、本地滚动保留与七天远端生命周期。
主机告警更新共享实现，prod 绑定实际数据卷；告警直接通知飞书，不写 S3。
媒体没有启用 offload、没有在用的历史 S3 指针，清理默认 AWS 注入和休眠配置。

本次不销毁 AWS 数据、角色、桶、KMS 或实例，不重启或切流主网关、Caddy、PG、Redis。
历史 QA raw 不搬迁；其恢复与到期边界继续由 QA 运维入口拥有。

## 身份、加密与恢复

数据库备份包含账号凭据，继续保留原 AWS AES256 的静态加密保护。Hetzner 采用
SSE-C；这不改变普通 QA raw 不使用业务加密的决定。每个节点使用独立随机 32 字节
备份加密密钥。现有项目 S3 凭据按用户授权复用，故它们不是节点级 IAM 隔离。
节点前缀校验用于防误操作，不能声称凭据只允许一个前缀。

`/etc/tokenkey/pgdump-storage.json` 只存目标、URI、HTTPS endpoint、region 与文件引用。
凭据和 SSE-C 密钥为 root-only 文件，位于网关挂载目录之外，既不进 `.env`、容器、
SSM Run Command 参数，也不写日志或命令行。通过验证 host key 的 SSH 传输。
恢复材料同时保留在操作机受保护目录与 SSM SecureString；SSM 仍是现有控制面依赖，
不能把这次数据平面迁移描述成零 AWS。灾备必须先取回对应节点密钥，再读 S3 对象。

显式配置不存在时保留 AWS standby 兼容；存在但坏配置、错误目标、缺凭据/密钥、
HTTP/未知 endpoint 一律失败，不回退 AWS 默认身份。上传与恢复复用
`ops/stage0/pgdump_storage.py`，AWS CLI 只作为 S3 协议客户端。

## 切换与回滚

`ops/stage0/provision_hcloud_pgdump_bucket.py` 创建/校验私有、TLS-only 桶，验证
普通及 multipart SSE-C 上传下载、无密钥/错误密钥拒绝、匿名拒绝与合成数据清理。
仅删除本次生成的合成对象。

`ops/stage0/install_hcloud_pgdump_storage.py` 默认 plan；prepare 校验现网矩阵、
主机身份、prod 卷与 timer，将最近已有真实备份上传到候选存储，不额外执行 pg_dump。
既有 canary 使用候选 config 与本地原文件核对 SHA-256，隔离恢复真实数据库。
只有匹配该目标、对象、endpoint、哈希及清理完成的最近回执才允许 activate。
切换前保存旧脚本、配置和 URI；距下一次 timer 不足五分钟或任务正在运行则拒绝切换。

恢复容器无网络、不拉镜像、最多半核/1GiB，不使用额外 swap；切换验证显式限制
块设备读写各 10MiB/s，宿主任务低优先级运行。恢复前核查内存余量与实际磁盘，
已有 canary 按 SQL 体积做容量门禁。逐台验证；任一失败停止后续节点。
一次性恢复库关闭 fsync、同步提交和 full-page writes，减少重复 WAL/刷盘；它不承担
持久运行或崩溃恢复职责。主数据库参数保持原值，完整 SQL 恢复、对象哈希、数据核对及
清理验收仍是成功回执的必要条件；临时库中途崩溃仍按验证失败处理。
大 prod 在该资源上限下允许 SQL 恢复最多一小时、临时库数据核对最多十分钟；
读取主库的核对仍保持原两分钟上限。外层 SSM/workflow 留出下载与清理时间。
activate 原子安装 helper/script 与 config，不启停 timer。rollback 恢复存储身份和
上传脚本和对应恢复回执，仅恢复本次修改的 URI，保留其他后来修改的 `.env` 字段。
安装中途失败自动回滚并传播失败；旧 AWS 备份保留。

## 测试与运行证据

安全和失败行为见 `ops/stage0/test_pgdump_storage.py`；真实恢复契约见
`ops/stage0/test_pgdump_restore_canary.py`。迁移验收对应 US-063。
现场回执和敏感恢复文件只保存操作机受保护目录及主机，不提交临时审计报告。
上线入口与存储坐标由 `deploy/hetzner/README.md` 单一维护。
