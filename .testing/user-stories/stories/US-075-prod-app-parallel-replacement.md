# US-075-prod-app-parallel-replacement

- ID: US-075
- Priority: P0
- Title: prod 应用机并行替换的隔离准备与启动保护
- As a / I want / So that: 作为生产运维，我要在旧应用继续服务时验证普通根盘候选，从而不为搬盘主动截断用户请求。
- Trace: `docs/approved/design-prod-app-parallel-replacement.md`；角色能力、候选生命周期与重启事件。
- Risk Focus:
  - 逻辑错误：存储契约与精确 SSM 身份绑定，候选不能被默认 resolver 误选。
  - 行为回归：旧 LUKS/蓝绿部署默认值不变；两色均使用可配置内存上限。
  - 安全问题：错误 UUID、mapper、嵌套挂载、符号链接和公开环境文件拒绝执行；ACL 只追加同网段精确来源。
  - 运行时问题：启动先于 HTTP ready 时有界等待；超时失败关闭；来源重试幂等且不删除旧规则。

## Acceptance Criteria

1. AC-001 (正向): Given 合法根盘候选 When 生成部署契约 Then 精确 SSM 身份可用且 Docker/application 两个启动 guard 都持久化。
2. AC-002 (负向): Given 错误存储 UUID、mapper、目录或 SSM 身份 When 校验 Then 在主机写入前拒绝。
3. AC-003 (回归): Given 旧 LUKS 主机与现有蓝绿部署 When 生成/启动 Then 原存储验证与原内存默认值保持不变。
4. AC-004 (正向/负向): Given app 尚未 ready When 启动控制别名 Then 等待 HTTP 成功再验证别名；持续不可用必须非零退出。
5. AC-005 (正向/负向): Given 已提交 ACL When 重复追加新来源 Then 原来源保留、新来源唯一；未提交或跨网段来源拒绝。
6. AC-006 (正向/负向): Given 运行中的代理与已审核 unit When 追加来源 Then 保留原来源、终止规则和进程；非规范 IPv4 或未知 unit 拒绝。

## Assertions

核对实际生成的 Compose/SSM 契约、文件前置保护、启动 shell 的调用顺序与超时退出、
来源配置落盘和幂等状态；不以文件存在或 `/health` 单项代表迁机完成。
生产接流、真实 usage、归档 singleton 与旧机保留另按批准方案验收，本 Story 不代表切流已完成。

## Linked Tests

- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_candidate_manifest_is_pinned_and_persists_startup_guard` (AC-001, AC-002)
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_ordinary_root_requires_pinned_identity_and_private_environment` (AC-002)
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_unmounted_prod_stops_before_host_writes` (AC-002)
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_tencent_luks_mapper_accepted_and_hetzner_identity_refused` (AC-003)
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_both_colors_allow_smaller_memory_without_changing_legacy_defaults` (AC-003)
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_root_boot_alias_waits_for_http_and_fails_closed` (AC-004)
- `ops/stage0/test_dataplane_source_acl.py`::`DataSourceACLTest.test_add_source_requires_committed_receipt_and_is_idempotent` (AC-005)
- `ops/stage0/test_dataplane_source_acl.py`::`DataSourceACLTest.test_additive_sources_preserve_old_host_and_validate_private_config` (AC-005)
- `ops/stage0/test_proxy_source_add.py`::`ProxySourceTest.test_source_addition_preserves_old_sources_and_is_idempotent` (AC-006)
- Run: `python3 -m unittest ops.stage0.test_bluegreen_host_runtime ops.stage0.test_deploy_via_ssm_bluegreen ops.stage0.test_resolve_prod_ssm_target ops.stage0.test_dataplane_source_acl ops.stage0.test_proxy_source_add`

## Evidence

生产隔离验证与未完成项见批准方案「实际证据」。当前没有改 DNS、默认发版目标或 QA owner。
无 UI 变更；真实 HTTPS/SSE 属运行集成验证，不冒充 Playwright e2e。

## Status

- InTest
