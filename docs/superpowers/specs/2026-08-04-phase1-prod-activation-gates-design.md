# 第一阶段生产启用门禁修复设计

## 背景

PR #1536 已合并第一阶段的数据保护与归档基础能力，但只读生产核查发现四个启用阻塞项：

- 安全探针固定读取旧的 tokenkey 应用容器，而生产环境使用蓝绿容器；
- 每日诊断对实例的第一个块设备做快照检查，没有使用 CloudFormation 输出的 DataVolumeId；
- 当前容量探针执行无边界的 COUNT(*) 扫描；
- 分区维护只在每日 cron 触发，部署新版本后不能立即修复缺失的当前及未来分区。

本 PR 只修复上述门禁。它不部署代码、不运行生产命令、不修改配置、不删除数据，也不授权解除归档清理暂停。

## 方案比较

### 采用：聚焦修复启用门禁

将已批准的有界容量探针原型提升为正式探针，复用仓库既有的蓝绿容器解析逻辑，从 stack 输出解析快照卷，并新增一个必须显式调用的一次性分区维护命令。

该方案的运行时改动最小，所有写行为仍封闭在独立、显式确认的运维命令中。

### 不采用：应用启动时自动维护分区

该方案最终也能修复缺失分区，但会让每次重启都附带 DDL 和锁竞争路径，并把维护风险带入请求服务进程；同时无法生成确定性的运维回执。

### 不采用：新增管理 API

管理 API 会为一个低频运维动作引入鉴权、授权、路由和公共契约。使用本地运维命令更简单，也更容易做到失败即关闭。

## 设计

### 活跃应用容器解析

probe-data-layer-safety.sh 默认使用 APP_CONTAINER=auto：

1. 优先读取 /var/lib/tokenkey/active-color。
2. 只接受 blue 或 green，并验证对应 tokenkey-blue 或 tokenkey-green 容器存在。
3. 若无法从 active-color 解析，则依次尝试 tokenkey、tokenkey-blue、tokenkey-green。
4. 保留显式 APP_CONTAINER，用于诊断和测试。

如果无法解析应用容器，探针必须输出不可用信号；禁止静默读取任意已停止或不存在的容器。

### 快照目标

每日诊断从已查询的 CloudFormation stack 输出中读取 DataVolumeId。输出缺失时，生成“快照信号缺失”，让安全判定失败关闭。

禁止回退到 BlockDeviceMappings[0]，因为当前 stack 中该位置是根卷，不是 PostgreSQL 持久数据卷。

### 有界容量信号

正式探针采用已批准原型的保护契约：

- PostgreSQL 会话设置 default_transaction_read_only=on；
- lock_timeout=100ms，statement_timeout=2s；
- usage_logs 总行数使用 pg_stat_user_tables 估算；
- 表已分区时，关系大小和行数估算汇总叶子分区；
- 只执行一次有边界的近 30 天查询，同时得出 30 天和 7 天增长量；
- catalog 查询失败、查询超时、统计缺失或数值无效时返回 unknown，禁止猜测为 green。

现有阈值和每日 workflow 调度频率不变。

### 一次性分区维护

新增独立的生产运维 CLI，复用仓库既有的 SSM 传输模式，在 Stage0 主机运行远端实现。执行必须提供精确确认词：

tokenkey-prod-partition-maintenance-v1

远端实现必须：

- 设置 lock_timeout=100ms 和 statement_timeout=5s；
- 修改前确认每个目标确实是分区表；
- 只为 ops_system_logs、ops_error_logs 和 usage_logs 创建当前及未来分区；
- 与应用内维护 owner 使用相同窗口：ops 表创建当前月加未来三个月，usage 表创建当天加未来七天；
- 遇到已存在或范围重叠的分区时，必须通过 catalog 再次证明目标时间范围已经被覆盖，才可视为成功；
- 禁止 DELETE、DROP、TRUNCATE、DETACH、表重写、容器重启和配置修改；
- 只有全部目标范围验证通过后，才写入现有 ops_partition_maintenance 成功心跳；
- 返回字段化 JSON 回执，并明确 deletion_authorized=false。

发生锁超时、未知 schema、时间范围未覆盖、回执格式错误或传输目标不明确时，必须以非零状态失败关闭。

## 验证

行为测试必须证明：

- 蓝绿解析能选择活跃应用容器；没有可用容器时失败关闭；
- workflow 使用 DataVolumeId 查询快照，且不包含按块设备位置取卷的逻辑；
- 容量 SQL 包含只读和超时保护，不执行全表计数；信号不完整或无效时返回 unknown；
- 分区维护命令拒绝缺失或错误的确认词，生成固定窗口、带超时且只创建分区的 SQL，校验远端回执，并且不包含破坏性 SQL；
- 现有数据层判定、workflow、分区和 preflight 检查继续通过。

所有测试和验证均不得连接生产环境。

## 不做

- 不执行生产命令，不部署、不重启、不执行 schema 迁移、不修改 setting。
- 不启用 telemetry，不解除 cleanup hold，不调整保留期，不删除归档数据，不扩容，也不推进 RDS。
- 不修改请求处理、计费、鉴权或任何用户可见 API。
