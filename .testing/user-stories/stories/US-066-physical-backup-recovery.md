# US-066-physical-backup-recovery

- ID: US-066
- Title: 用户账目可通过物理增量与连续 WAL 恢复
- Priority: P1
- As a / I want / So that: 作为运维人员，我要两小时增量备份和可验证的恢复，从而在故障后恢复用户明细及账目。
- Trace: `docs/approved/design-prod-physical-backup.md`
- Risk Focus:
  - 逻辑错误：备份依赖、指定恢复点、余额与幂等键一致性。
  - 行为回归：旧 pgdump 数据保留；独立授权退役任务，cn-prod 及现有 QA 暂停保持。
  - 安全问题：恢复拒绝生产宿主，仓库只读身份，无公开端口，不运行应用。
  - 运行时问题：WAL 缺口、文件损坏、仓库不可达、互斥、过期回执和资源清理失败。

## Acceptance Criteria

1. AC-001 (正向): Given 全量后数据变更 When 两次增量和同点双路径恢复 Then 明细、余额、分区、索引和序列符合期望。
2. AC-002 (负向): Given WAL 缺口或备份损坏 When 校验/恢复 Then 失败且不伪报连续可恢复。
3. AC-003 (正向): Given 恢复副本 When 重试真实计费/退款 Then 热表与归档幂等且余额不重复变化。
4. AC-004 (回归): Given 旧数据库配置 When 准备新配置 Then 只修改备份变量，增量仍每两小时；安装器自身不切换 pgdump；退役由另一个显式授权入口执行。
5. AC-005 (负向): Given 生产数据宿主或清理失败 When 恢复 Then 拒绝/失败，不发布成功回执。
6. AC-006 (负向): Given 新备份但 WAL 过期或恢复证据缺失/失败 When 监控 Then 故障告警；只有曾成功而过期的恢复/仓库验证降为每日提醒，不把旧证据当新验证。

7. AC-007 (正向): Given 已验收最新/历史恢复和至少 30 天 S3 留存 When 退役 prod pgdump Then 只停旧 timer、阻止旧导出，数据保留且不重启生产容器。
8. AC-008 (负向): Given 备份不健康、留存不足、dump 在跑、锁忙或上次操作未完成 When 退役 Then 拒绝，不宣称成功或终止在途任务。

## Assertions

验证实际恢复数据与期望精确一致，指定点之后事务不存在；真实 repository 的扣费/退款重试不重复影响余额；
缺 WAL 与损坏文件必须失败；独立资源销毁后才能生成回执。30 分钟/七天为待生产实测目标。

## Linked Tests

- `ops/stage0/rehearse_postgres_backup.py`::`Rehearsal.exercise` (AC-001, AC-002, AC-003)
- `ops/stage0/test_postgres_backup.py`::`PhysicalBackupTests.test_wal_gap_and_timeline_change_are_not_healthy` (AC-002)
- `ops/stage0/test_postgres_backup.py`::`PhysicalBackupTests.test_installer_preserves_settings_and_keeps_two_hour_timer` (AC-004)
- `ops/stage0/test_postgres_backup.py`::`PhysicalBackupTests.test_restore_requires_prior_incremental_and_never_uses_live_volume` (AC-005)
- `ops/stage0/test_postgres_backup.py`::`PhysicalBackupTests.test_restore_cleanup_failure_cannot_become_success` (AC-005)
- `ops/observability/test_host_https_watch.py`::`PhysicalBackupWatchTest.test_failed_or_stale_chain_alerts_despite_successful_backup_job` (AC-006)
- `ops/observability/test_host_https_watch.py`::`PhysicalBackupWatchTest.test_only_expired_successful_evidence_is_advisory` (AC-006)
- `ops/observability/test_host_https_watch.py`::`PhysicalBackupWatchTest.test_validation_reminder_is_daily_and_never_masks_a_new_fault` (AC-006)
- `ops/stage0/test_retire_pgdump.py`::`RetirementTests.test_plan_is_read_only_then_apply_and_recheck` (AC-007)
- `ops/stage0/test_retire_pgdump.py`::`RetirementTests.test_running_dump_and_partial_attempt_refuse` (AC-008)
- `ops/stage0/test_retire_pgdump.py`::`RetirementTests.test_lock_busy_cannot_change_timer` (AC-008)
- `ops/stage0/test_postgres_backup.py`::`PhysicalBackupTests.test_retirement_requires_live_physical_health_and_both_restore_paths` (AC-008)
- `ops/observability/test_postgres_backup_verdict.py`::`VerdictTests.test_current_degraded_stale_and_missing_health` (AC-006)
- Run: `python3 ops/stage0/test_retire_pgdump.py`
- Run: `python3 ops/stage0/test_postgres_backup.py`
- Run: `python3 ops/observability/test_host_https_watch.py`
- Run: `python3 ops/stage0/rehearse_postgres_backup.py --image tokenkey-postgres-backup-test:dev --storage s3 --billing-check /tmp/backup-billing-check --receipt /tmp/physical-backup-rehearsal.json`

## Evidence

TLS S3 原型已运行全量、两次增量、同点双路径恢复及缺 WAL/损坏文件负向测试。
ARM64 真实计费二进制已在三份恢复副本上通过热表/归档重复扣费和退款验收。Python 行为与告警测试通过。生产 AWS 真实最新与历史同点全量/增量恢复已通过，临时资源已清理；详见 `ops/stage0/physical-backup.md`。七天覆盖和持续性能观测仍未完成，状态保持 InTest。

## Status

- InTest
