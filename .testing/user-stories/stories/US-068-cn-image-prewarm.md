# US-068-cn-image-prewarm

- ID: US-068
- Priority: P1
- Title: cn-prod 私有镜像预下载
- As a / I want / So that: 作为运维人员，我要 CI 自动中转校验过的镜像并预下载，从而减少发布等待且保持在线服务。
- Trace: `docs/approved/cn-prod-image-distribution.md`
- Risk Focus:
  - 逻辑错误：源摘要、架构、版本和 revision 必须与归档一致。
  - 行为回归：GHCR 显式备用路径保留，不隐式切流。
  - 安全问题：拒绝篡改、加载无关标签或覆盖不同工件；短期签名不进入回执。
  - 运行时问题：锁忙拒绝下载、下载失败不导入、幂等缓存、临时文件清理。

## Acceptance Criteria

1. AC-001 (正向): Given 合法 amd64 发布镜像 When 上传并预下载 Then 校验源 config/layers、manifest 最后上传、导入后绑定版本标签。
2. AC-002 (负向): Given 错误架构/版本或篡改归档 When 校验 Then 拒绝导入。
3. AC-003 (负向): Given 下载失败或主机锁忙 When 预下载 Then 不调用 Docker 导入，不操作服务。
4. AC-004 (回归): Given 已验缓存或显式 GHCR When 重试 Then 缓存避免下载且旧路径仍校验架构。
5. AC-005 (负向): Given 同 key 不同内容或已有不同镜像标签 When 发布 Then 不覆盖旧工件/标签。

6. AC-006 (负向): Given Release 元数据指向错误 tag/revision 或包含命令文本 When 解析 Then 拒绝且不执行发布 ref 的代码。

## Assertions

核对源 config/layers、实际调用副作用序列、错误异常、标签不可覆盖、缓存回执与临时文件清理。

## Linked Tests

- `deploy/domestic/test_image_distribution.py`::`DistributionTests.test_publish_manifest_last_and_remote_script_executes` (AC-001)
- `deploy/domestic/test_image_distribution.py`::`ArchiveTests.test_warm_downloads_verifies_loads_and_caches_without_service_mutation` (AC-001, AC-004)
- `deploy/domestic/test_image_distribution.py`::`ArchiveTests.test_identity_and_tampered_archives_fail_closed` (AC-002)
- `deploy/domestic/test_image_distribution.py`::`ArchiveTests.test_bad_download_never_reaches_docker` (AC-003)
- `deploy/domestic/test_image_distribution.py`::`ArchiveTests.test_busy_host_lock_does_not_download` (AC-003)
- `deploy/domestic/test_deploy_via_ssm.py`::`DeployViaSsmTest.test_ssm_pull_executes_under_posix_shell_and_enforces_architecture` (AC-004)
- `deploy/domestic/test_image_distribution.py`::`DistributionTests.test_immutable_retry_and_conflict` (AC-005)
- `deploy/domestic/test_image_distribution.py`::`ArchiveTests.test_wrong_loaded_revision_or_existing_tag_is_not_tagged` (AC-005)
- `deploy/domestic/test_image_distribution.py`::`DistributionTests.test_release_artifact_is_only_data_and_must_match_git_tag` (AC-006)
- Run: `python3 -m unittest scripts.test_cn_image_distribution -v`

## Evidence

本地行为测试覆盖分发与失败保护；合并后的首次 release CI/OIDC/主机预下载回执仍需验收。
无 UI 变更，不适用 Playwright e2e。

## Status

- InTest
