# US-062 Edge 快照迁移单次注册

- ID: US-062
- Priority: P1
- Title: 保留原机的 Edge 迁移注册准备
- As a / I want / So that: 作为运维，我希望使用现有部署角色准备单次新机身份，以完成保留数据的快照迁移而不扩大 operator IAM 权限。
- Trace: `docs/approved/edge-8g-snapshot-resize.md` §新机注册准备。
- Risk Focus:
  - 逻辑错误：源目标、region 与新机存在性判断错误。
  - 行为回归：准备步骤意外执行普通 provision、部署或调度切换。
  - 安全问题：凭据泄漏、角色或参数前缀越界、覆盖既存凭据。
  - 运行时：网络失败误判不存在、加密交接失败留下有效注册凭据。

## Acceptance Criteria

1. AC-001（正向）：确认源目标后，生成一次/四小时有效身份，以 SecureString 交接；安全结果不含 code，不改变机器或路由。
2. AC-002（负向）：既存新机、权限/传输未知、相同新旧名、非法 run 标识、源目标不符均拒绝创建身份。
3. AC-003（回归）：加密交接失败仅撤销本次 activation；原机身份和已有参数不被覆盖。

## Assertions

比较实际 AWS 调用序列、角色与参数前缀、注册次数、文件权限、结果脱敏以及失败时撤销对象。

## Linked Tests

- AC-001: `ops/lightsail/test_prepare_resize_identity.py`::`test_single_use_scoped_identity_and_encrypted_handoff`
- AC-002: `ops/lightsail/test_prepare_resize_identity.py`::`test_existing_replacement_or_uncertain_lookup_prevents_mutation`
- AC-002: `ops/lightsail/test_prepare_resize_identity.py`::`test_invalid_source_replacement_and_run_identity_fail_closed`
- AC-003: `ops/lightsail/test_prepare_resize_identity.py`::`test_failed_handoff_revokes_only_new_activation`
- Run command: `python3 -m unittest discover -s ops/lightsail -p test_prepare_resize_identity.py -v`

## Evidence

四项行为测试与完整 preflight 通过。us3/us4/us5/us6 的 prepare-resize workflow 均 success，run 链接见 `docs/ops/edge-image-capacity-upgrade-20261009.md`。本 Story 只覆盖注册准备，不代表实例迁移已完成。

## Status

- Done
