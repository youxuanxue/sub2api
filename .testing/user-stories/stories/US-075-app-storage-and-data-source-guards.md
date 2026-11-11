# US-075-app-storage-and-data-source-guards

- ID: US-075
- Priority: P1
- Title: 应用根盘迁出准备与数据机来源保护
- As a / I want / So that: 作为运维人员，我要验证复制与启动身份并限制数据端口来源，从而安全解除应用机解密依赖且避免数据库误开放。
- Trace: `docs/approved/design-data-plane-split.md`；2026-11-11 用户「同意建议」。
- Risk Focus:
  - 逻辑错误：根盘、兼容 bind、UUID 和运行状态必须一致；在线副本不能当作一致副本。
  - 行为回归：默认 LUKS 保护与正常发版重装 guard 保持一致。
  - 安全问题：错误设备、缺失状态、未知 Hybrid 身份拒绝；只放行本站应用来源。
  - 运行时问题：复制失败持久留回执；来源规则有自动回滚，Docker 重启后重装。

## Acceptance Criteria

1. AC-001 (正向): Given 正确根盘与兼容 bind When 检查 Then 通过；发版向两个服务安装同一存储契约。
2. AC-002 (负向): Given 错误 UUID/mapper/挂载/空目录/未知主机 When 检查或渲染 Then 拒绝启动或投递。
3. AC-003 (回归): Given 当前站点矩阵仍用 LUKS When 发版渲染 Then 保持 LUKS 契约。
4. AC-004 (负向): Given 复制失败或配置漂移 When 准备 Then 保存失败且拒绝继续，不宣称一致。
5. AC-005 (正向/负向): Given 本站应用及其它来源 When 连接 DNAT 或主机数据端口 Then 只允许本站应用并保留本地访问；其它端口不受影响，回滚恢复连接。
6. AC-006 (正向/负向): Given 已批准窗口与停写一致副本 When 执行切换 Then 保留原容器、匿名卷与备用色状态；窗外拒绝执行，接流后的回退须先停写并同步新状态。

## Assertions

观察检查结果、实际生成的 systemd guard、失败回执、真实 TCP 连接和规则回滚结果。复制演练核对真实文件内容、权限、ACL、xattr、hardlink、sparse 与反向同步。

## Linked Tests

- `ops/stage0/test_root_bind_storage.py`::`RootBindStorageTest.test_canonical_root_and_same_directory_bind_pass` (AC-001)
- `ops/stage0/test_root_bind_storage.py`::`RootBindStorageTest.test_hybrid_delivery_matches_live_matrix_and_guard_preserves_contract` (AC-001, AC-003)
- `ops/stage0/test_root_bind_storage.py`::`RootBindStorageTest.test_wrong_uuid_device_mount_or_empty_state_rejected` (AC-002)
- `ops/stage0/test_root_bind_storage.py`::`RootBindStorageTest.test_unknown_hybrid_host_cannot_receive_a_storage_contract` (AC-002)
- `ops/stage0/test_prepare_app_luks_exit.py`::`PrepareLuksExitTest.test_copy_failure_is_persisted_without_secret_output` (AC-004)
- `ops/stage0/test_prepare_app_luks_exit.py`::`PrepareLuksExitTest.test_workspace_refuses_unowned_files_symlinks_and_configuration_drift` (AC-004)
- `ops/stage0/test_dataplane_source_acl.py`::`DataSourceACLTest.test_only_original_direction_published_data_ports_are_filtered` (AC-005)
- `ops/stage0/test_cutover_app_luks_exit.py`::`CutoverLuksExitTest.test_only_exact_approved_window_and_confirmation_can_apply` (AC-006)
- `ops/stage0/test_cutover_app_luks_exit.py`::`CutoverLuksExitTest.test_restart_preserves_standby_and_checks_identity_before_starting_any_writer` (AC-006)
- `ops/stage0/test_cutover_app_luks_exit.py`::`CutoverLuksExitTest.test_final_copy_uses_checksum_and_does_not_replace_held_locks` (AC-006)
- `ops/stage0/test_cutover_app_luks_exit.py`::`CutoverLuksExitTest.test_rollback_after_start_syncs_new_state_before_restoring_old_mounts` (AC-006)
- `ops/stage0/test_cutover_app_luks_exit.py`::`CutoverLuksExitTest.test_failed_reverse_sync_cannot_start_the_stale_copy` (AC-006)
- `ops/stage0/test_cutover_app_luks_exit.py`::`CutoverLuksExitTest.test_acceptance_requires_real_metering_and_capture_after_ingress` (AC-006)
- Run: `python3 -m unittest ops.stage0.test_root_bind_storage ops.stage0.test_prepare_app_luks_exit ops.stage0.test_dataplane_source_acl`
- Runtime: disposable Linux container with rsync/acl/attr runs `python /code/rehearse_app_storage_copy.py`.
- Runtime: privileged disposable Linux container with iptables/iproute2 and no external network runs `unshare --net python /code/rehearse_dataplane_source_acl.py` (AC-005).

## Evidence

专项单元通过；隔离 Linux 文件复制/内容校验和反向同步通过；两站隔离真实 TCP 的 INPUT/DNAT、允许与拒绝来源、无关端口、重复应用和回滚通过。生产迁出、Docker 重启与维护窗尚未执行。

## Status

- InTest
