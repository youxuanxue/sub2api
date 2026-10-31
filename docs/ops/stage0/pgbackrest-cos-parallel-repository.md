---
title: pgBackRest AWS repo1 与腾讯 COS repo2 并行迁移草案
status: draft
---

# 目的

迁移期让同一份 PostgreSQL 全量、增量和 WAL 同时写入现有 AWS S3 `repo1` 与腾讯 COS `repo2`。AWS 链保留为回退来源；只有在 COS 完成远端 `check`、`verify`、最新点和历史点隔离恢复验收后，才允许把 COS 提升为主仓库。本文只提供可审查的配置渲染入口，不创建 COS 桶、不写入远端、不启用 systemd timer。

## 配置边界

`ops/stage0/postgres_backup.py config` 默认仍只渲染现有 AWS `repo1`。传入完整的 `--secondary-*` 参数后才增加 `repo2`；凭据永远不由渲染器生成或写入，身份刷新器在受控主机上注入短期凭据。repo1 的桶、路径、区域和保留策略不会被 repo2 覆盖。

这是配置草案，不会让现有 `backup`/`check` 命令自动写入 repo2。pgBackRest 的多仓库行为必须在单独变更中显式指定：备份验收阶段使用 `--repo=all backup`、`--repo=all check` 和 `--repo=all verify`，归档命令需确认镜像中的 pgBackRest 版本支持 `--repo=all archive-push`，再通过隔离环境证明两个仓库均收到同一 WAL。未完成该验证前，线上命令继续只使用默认 repo1；不得仅凭生成出的配置宣称双写已启用。

示例（只输出配置到 stdout）：

```bash
PYTHONPATH=ops/stage0 python3 ops/stage0/postgres_backup.py config \
  --bucket tokenkey-prod-physical-backup-682751977094-us-east-1 \
  --region us-east-1 \
  --repo-path /candidates/tencent-usw-20261022/postgres \
  --secondary-bucket tokenkey-prod-cos-<appid> \
  --secondary-region na-siliconvalley \
  --secondary-endpoint cos.na-siliconvalley.myqcloud.com \
  --secondary-path /prod/postgres \
  --secondary-uri-style path
```

cn-prod 使用相同入口，AWS repo1 保持现网东京仓库，COS repo2 改用北京 COS endpoint 和独立前缀。正式值以云账号实际创建的桶名、地域和密钥策略为准，不能从示例占位符直接执行。

## 上线顺序与硬门禁

1. 先在合成数据和 COS 兼容测试存储上验证 pgBackRest 版本、S3 签名、path-style、WAL 归档和并行仓库行为。
2. 生产变更时把 repo2 配置放入临时配置文件，执行 `stanza-create`、`check` 和一次只读 `info`；确认 repo1 仍可读后，再按维护窗口启用双写。不得先删除、改名或缩短 AWS 留存。
3. 运行至少一份完整全量、一轮自然增量及连续 WAL；对两个仓库分别执行 `check`/`verify`，并从 COS 恢复最新点和一个历史点到全新隔离目录。验收回执要记录仓库、备份 label、WAL 覆盖起点、校验结果和耗时。
4. COS 验收未通过时，停用 repo2 写入并保留 AWS repo1；不得删除 COS 上的失败对象或 AWS 链。COS 验收通过后仍至少保留 AWS 只读回退链 30 天，再单独审批退役。

在 pgBackRest 版本、COS 签名和双仓库归档语义未完成合成数据验证前，不得执行 `--repo=all`，不得把 `repo2` 配置复制到线上运行目录，也不得切换默认仓库。

## 安全不变量

- `backup`、`archive-push` 和 `check` 必须对两个仓库成功；一个仓库失败时整体状态为失败/降级并保留本地 WAL，不能伪报成功。
- 所有复制和验证任务使用低优先级、带宽上限及临时目录；配置和回执通过原子替换发布。
- 不重启网关、PostgreSQL 或 Redis，不启动新 timer，不在本草案中创建付费云资源。
- 不使用 `repo2` 覆盖 `repo1` 的路径、凭据或 retention；不在仓库提交任何 COS secret、token 或签名 URL。
- 迁移期间不得执行 `expire`、桶删除或前缀清理；AWS 退役必须有独立审批和可回退证据。

## 当前阻塞点

仓库代码没有现成的腾讯 COS 凭据刷新器、桶策略和生产变更入口；因此本变更只交付无密钥的双 repo 配置渲染与校验。创建 COS 资源、配置 IAM/密钥、改写线上 host 配置和启用双写需要后续独立变更单及现场验收，不能由本文件或单元测试推断已经完成。
