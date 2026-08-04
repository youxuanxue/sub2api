---
title: 第一阶段生产启用门禁修复
status: approved
approved_by: "xuejiao (conversation approval, 2026-08-04)"
approved_at: 2026-08-04
authors: [agent]
created: 2026-08-04
risk: high
related_prs: []
related_commits: []
---

# 第一阶段生产启用门禁修复

## 决策

PR #1536 合并了数据保护与归档基础能力，但生产只读核查发现四个启用阻塞项：

1. 安全探针读取旧的 tokenkey 容器，而生产使用蓝绿容器。
2. 每日诊断检查根卷快照，没有检查 PostgreSQL 持久数据卷。
3. 正式容量探针执行无边界 COUNT(*)。
4. 分区维护只有每日 cron，没有可控的一次性修复入口。

本 PR 只修复这四个门禁。merge 不部署、不重启、不执行生产命令、不修改线上配置，也不解除 cleanup hold。

## 聚焦边界

只保留三个交付物：

1. 修正只读数据层诊断。
2. 将已批准的有界容量原型替换为唯一正式实现。
3. 为现有 Go 分区维护 owner 增加显式确认的一次性入口。

不新增管理 API，不在应用启动时自动执行 DDL，不新增第二套分区 SQL 引擎，也不让只读 probe 通道承载写操作。

## 只读诊断

### 活跃应用容器

probe-data-layer-safety.sh 默认使用 APP_CONTAINER=auto：

1. 读取 /var/lib/tokenkey/active-color，只接受 blue 或 green。
2. active-color 指向的容器必须处于 running 状态才可选中。
3. active-color 缺失、无效或指向非运行容器时，枚举 tokenkey、tokenkey-blue、tokenkey-green 的 running 状态。
4. 只有恰好一个候选处于 running 时才允许 fallback。
5. 零个或多个候选处于 running 时返回 unknown，不按固定顺序猜测。
6. 显式 APP_CONTAINER 也必须验证为 running；否则返回 unknown。

探针只读取最终选中容器的 TELEMETRY_ARCHIVE_ENABLED。解析失败不得影响应用容器，也不得猜测 telemetry 状态。

### 快照目标

每日诊断从 CloudFormation stack 输出读取 DataVolumeId，并据此查询最新 completed snapshot。

DataVolumeId 缺失或查询失败时，快照信号为 null，安全判定失败关闭。禁止使用 BlockDeviceMappings 的位置索引，也不回退到根卷。

### 有界容量

正式 probe/verdict 采用已批准原型的保护契约：

- PostgreSQL 会话设置 default_transaction_read_only=on；
- lock_timeout=100ms，statement_timeout=2s；
- usage_logs 总行数从 pg_stat_user_tables 估算；
- 分区表的关系大小和行数估算汇总叶子分区；
- 一次近 30 天有界查询同时计算 30 天和 7 天增长；
- catalog 失败、查询超时、统计缺失或非法数值均返回 unknown。

晋升方式是“替换”，不是“再复制一套”：

- 正式 probe-data-layer-capacity.sh 和 data_layer_capacity_verdict.py 接收原型能力；
- 测试改为直接覆盖正式文件；
- 删除两个 prototype 源文件；
- 阈值和每日 workflow 调度频率保持不变。

## 一次性分区维护

### 单一 owner

将现有 ensureOpsPartitions 能力收敛为一个可复用的 Go maintainer。每日 cron 和一次性命令必须调用同一 owner，由它唯一持有：

- 目标表：ops_system_logs、ops_error_logs、usage_logs；
- ops 窗口：当前月加未来三个月；
- usage 窗口：当天加未来七天；
- 创建、重叠处理和覆盖验证规则。

不得在 Python、shell 或 workflow 中复制目标表、窗口或 CREATE PARTITION SQL。

为兼容尚未完成分区迁移的非生产环境，cron 可以保留“非分区表跳过”行为；生产一次性模式必须启用严格验证，要求三张目标表全部已分区。maintainer 必须返回每张表的覆盖结果，禁止空操作被记录为成功。

### 发布产物内的一次性模式

现有 /app/sub2api 二进制增加一次性模式：

--partition-maintenance-once --confirm tokenkey-prod-partition-maintenance-v1

该模式在 setup 检查、migration runner、secret bootstrap、默认分组补齐和 HTTP server 启动之前分流，只执行以下动作：

1. 校验精确确认词。
2. 读取数据库连接配置。
3. 直接建立专用 PostgreSQL 连接；禁止调用 repository.InitEnt。
4. 在同一连接设置 lock_timeout=100ms 和 statement_timeout=5s。
5. 以严格模式调用共享 Go maintainer，创建并验证当前及未来分区。
6. 全部验证通过后写入 ops_partition_maintenance 成功心跳。
7. 输出字段化 JSON 回执并退出。

命令不得运行 DELETE、DROP、TRUNCATE、DETACH、历史数据复制、表重写、容器重启或配置修改。确认失败、锁超时、未知 schema、范围未覆盖或心跳失败均返回非零状态。

### 独立写侧 controller

新增固定用途的本地 operator controller，生产目标硬编码为 tokenkey-prod-stage0：

1. 本地先校验同一个精确确认词。
2. 从 CloudFormation 解析 InstanceId。
3. 通过 SSM 只发送固定的 partition-maintenance 命令，不接受任意 script、command 或 target 参数。
4. 远端根据 active-color 选择 running 容器；出现歧义时拒绝。
5. 使用非 root 用户在活跃容器中执行 /app/sub2api 一次性模式。
6. 校验 SSM 目标、退出状态和 JSON 回执，并写入不可覆盖的本地 receipt。

controller 禁止调用 ops/observability/run-probe.sh。精确确认词在本地 controller 和远端二进制各校验一次。

合并 PR 不会调用该 controller。后续生产执行仍需单独人工批准。

## 验证

所有验证均在本地或 CI 完成，不连接生产环境：

- 容器解析覆盖 active-color 正向、目标未运行、唯一 fallback、零候选和多候选。
- workflow 测试证明只使用 DataVolumeId，且不存在位置式块设备查询。
- 容量测试证明只读/超时保护、无全表 COUNT(*)、异常信号返回 unknown。
- 晋升测试证明 prototype 文件删除且正式文件是唯一 consumer。
- Go maintainer 测试证明 cron 与一次性入口共享目标和窗口，只产生创建/验证/心跳动作。
- 一次性模式测试证明错误确认词不会打开数据库，且不会启动 migration 或 HTTP server。
- controller 测试证明目标和远端命令不可注入、错误确认词不发 AWS 请求、异常回执失败关闭。
- preflight 和相关数据层回归测试全绿。

## 不做

- 不执行任何生产写操作、部署、发布、重启或 setting 修改。
- 不启用 telemetry，不解除 cleanup hold，不修改归档保留期。
- 不删除数据，不扩容，不推进 RDS。
- 不修改请求处理、计费、鉴权或用户可见 API。

## 审批门禁

本设计已获人工确认，可以进入实现计划；实现完成后仍需独立合并审批。merge 不等于生产执行批准。
