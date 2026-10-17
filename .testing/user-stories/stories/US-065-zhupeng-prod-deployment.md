# US-065-zhupeng-prod-deployment

- ID: US-065
- Priority: P1
- Title: 独立 NAS prod 复用统一部署入口
- As a / I want / So that: 作为运维人员，我要显式选择 Node1 并复用蓝绿发布，从而在不改变现网 prod 的情况下验收替代国内站的环境。
- Trace: `docs/approved/node1-prod-precutover.md`
- Risk Focus:
  - 逻辑错误：目标选择、角色、主机和磁盘身份匹配。
  - 行为回归：现有 UCloud Caddy 路由和 SSM 发布保留。
  - 安全问题：未知目标拒绝、私钥不上传、独立秘密不输出、测试入口仅 loopback。
  - 运行时问题：数据盘失挂拒绝启动、切换失败保留回执边界、秘密重复初始化不漂移。

## Acceptance Criteria

1. AC-001 (正向): Given 合法 SSH prod When 渲染部署 Then 使用共享 prod payload，QA archive 关闭且不调用 AWS。
2. AC-002 (负向): Given 未知/禁用目标或错误主机/磁盘 When 部署前检查 Then 拒绝且不写数据。
3. AC-003 (回归): Given UCloud 或 SSH 隧道配置 When 真实 Caddy 接收产品路径 Then 仍转发给共享应用。
4. AC-004 (安全): Given SSH 测试目标 When 渲染 Compose Then 入口只绑定 loopback，秘密重复生成保持不变。
5. AC-005 (负向): Given HTTPS 健康检查失败 When 执行 smoke-only Then 非零退出，不被后续容器列表成功掩盖。
6. AC-006 (数据): Given 冻结的用户日用量清单 When 缺行、租户错位、费用/token/内容变化 Then 对账失败；空清单不能通过。
7. AC-007 (隔离): Given 恢复库或历史页面演练容器 When 使用外网、公开端口、额外权限或错误数据挂载 Then 拒绝继续。
8. AC-008 (探针): Given 显式 Node1 探针目标 When 投递共享探针 Then 验证 SSH 主机身份且不调用 AWS；错误主机或 AWS instance override 在投递前拒绝。
9. AC-009 (接管): Given 新域名正式目标 When 渲染待接管库存 Then 当前库存不变；正式 bootstrap 被拒绝，部署/探针缺审批或候选挂载检查时也拒绝。

## Assertions

观察拒绝时无主机写操作、真实 Caddy 的 HTTP 返回、最终端口绑定及独立环境变量。

## Linked Tests

- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_ssh_render_has_prod_payload_without_hetzner_volume_or_aws_execution` (AC-001)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_unknown_disabled_and_injected_target_rejected` (AC-002)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_wrong_machine_rejected_before_disk_commands` (AC-002)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_wrong_volume_rejected` (AC-002)
- `deploy/domestic/test_runtime.py`::`DomesticRuntimeTest.test_public_paths_remain_shared_application_owned` (AC-003)
- `deploy/domestic/test_runtime.py`::`SSHTunnelRuntimeTest.test_tunnel_bindings_and_isolated_runtime` (AC-004)
- `deploy/domestic/test_runtime.py`::`DomesticRuntimeTest.test_prepare_preserves_secrets_and_private_permissions` (AC-004)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_failed_https_smoke_cannot_be_masked_by_container_listing` (AC-005)
- `ops/migration/test_node1_candidate.py`::`UsageHistoryAuditTest.test_equal_global_totals_cannot_hide_wrong_tenant_or_changed_content` (AC-006)
- `ops/migration/test_node1_candidate.py`::`CandidateSafetyTest.test_other_container_cannot_share_candidate_data` (AC-007)
- `ops/migration/test_node1_candidate.py`::`HistoryIsolationTest.test_only_owned_namespace_and_mounts_are_accepted` (AC-007)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_shared_probe_dispatches_ssh_without_aws_or_instance_override` (AC-008)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_probe_host_guard_fails_before_delivery` (AC-008)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_production_inventory_is_render_only_and_cannot_bootstrap` (AC-009)
- `deploy/domestic/test_ssh_deploy.py`::`SSHDeployTests.test_public_host_checks_require_the_approval_and_candidate_mount_guard` (AC-009)
- Run: `python3 -m unittest deploy/domestic/test_ssh_deploy.py deploy/domestic/test_runtime.py`
- Run: `python3 -m unittest ops/migration/test_node1_candidate.py`

## Evidence

本轮切流前准备进行中；历史安装记录不作为本次 AWS 数据与历史 usage logs 验收。
未完成的在线验证不标为成功；正式切流不在本故事授权内。

## Status

- InTest
