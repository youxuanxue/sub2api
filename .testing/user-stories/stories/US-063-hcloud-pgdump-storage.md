# US-063-hcloud-pgdump-storage

- ID: US-063
- Priority: P1
- Title: Hetzner 数据库备份安全切换
- As a / I want / So that: 作为运维人员，我要数据库备份及恢复使用私有 Hetzner 存储，从而减少 AWS 数据平面依赖且不影响用户流量。
- Trace: `docs/approved/hcloud-pgdump-storage.md`
- Risk Focus:
  - 逻辑错误：上传与恢复身份一致、目标前缀严格匹配。
  - 行为回归：AWS standby 恢复保持兼容。
  - 安全问题：秘密仅文件引用、私有权限、错误 endpoint/目标拒绝。
  - 运行时问题：上传失败保留本地文件、恢复容量不足及清理失败拒绝发布回执。

## Acceptance Criteria

1. AC-001 (正向): Given 私有 Hetzner 配置 When 上传并恢复 Then 使用同一 endpoint/身份/SSE-C 密钥，核对对象与本地 SHA-256，清理后发布回执。
2. AC-002 (负向): Given 错误目标/endpoint/权限/密钥 When 加载配置 Then 拒绝且不使用 AWS 默认身份。
3. AC-003 (回归): Given 无显式 Hetzner 配置 When 恢复 AWS standby Then 保留原 S3 命令；上传错误不删除本地备份。
4. AC-004 (负向): Given 磁盘不足或清理失败 When canary 执行 Then 不发布成功回执并保留上次回执。

## Assertions

观察实际生成的上传/下载命令和身份文件引用、拒绝行为、回执 SHA-256、清理结果、本地源文件保留。

## Linked Tests

- `ops/stage0/test_pgdump_storage.py`::`StorageTests.test_encrypted_upload_and_read_use_private_identity_despite_ambient_aws` (AC-001)
- `ops/stage0/test_pgdump_restore_canary.py`::`PgdumpRestoreCanaryTest.test_hetzner_restore_uses_encrypted_private_identity_and_verifies_round_trip` (AC-001)
- `ops/stage0/test_pgdump_storage.py`::`StorageTests.test_invalid_explicit_config_never_falls_back_to_valid_aws_env` (AC-002)
- `ops/stage0/test_pgdump_storage.py`::`StorageTests.test_secret_permissions_missing_key_and_symlink_fail_closed` (AC-002)
- `ops/stage0/test_pgdump_storage.py`::`StorageTests.test_legacy_aws_restore_uses_original_command_without_hetzner_headers` (AC-003)
- `ops/stage0/test_pgdump_storage.py`::`StorageTests.test_upload_failure_retains_local_source_and_redacts_provider_error` (AC-003)
- `ops/stage0/test_pgdump_restore_canary.py`::`PgdumpRestoreCanaryTest.test_insufficient_capacity_aborts_before_docker_run` (AC-004)
- `ops/stage0/test_pgdump_restore_canary.py`::`PgdumpRestoreCanaryTest.test_container_cleanup_failure_keeps_previous_receipt` (AC-004)
- Run: `python3 ops/stage0/test_pgdump_storage.py`
- Run: `python3 ops/stage0/test_pgdump_restore_canary.py`

## Evidence

自动化行为测试、完整 preflight 与 PR CI 已通过。prod、uk1、uk2、us4、us5
逐节点完成真实 dump 的加密上传/下载、本地 SHA-256 比对、完整 SQL 恢复、
核心数据核对与临时数据清理后激活；五节点正常定时任务的新备份均已写入 Hetzner，
远端对象大小及 SSE-C 状态核对通过。现场回执在操作机受保护目录，不提交数据文件。
多轮公共探活全部通过，活跃 gateway healthy、restart count 0；未重启 gateway/Caddy/PG/Redis。

## Status

- Done
