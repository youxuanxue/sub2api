# US-062 — QA uk1 独立队列与资源隔离

- ID: US-062
- Priority: P1
- Title: QA uk1 独立队列与资源隔离
- As a / I want / So that: 作为运维，我要 QA 在独立权限和有界资源内处理任务，以便迁出 SQS 并控制共用 Edge 的影响。
- Trace: `docs/approved/hcloud-bluegreen-qa-migration.md`；会话批准共用 uk1 的 1 CPU / 2 GiB 限额。
- Risk Focus:
  - 逻辑错误：重复提交、过期回执、投递次数和全局单租约。
  - 行为回归：默认 SQS、既有 RunOne 取消、Bundle 授权和输出契约。
  - 安全问题：队列角色越权、错库连接、TLS 降级、凭据泄露。
  - 运行时问题：并发领取、停止排空、暂停/恢复、数据库重启、资源超额。

## Acceptance Criteria

1. AC-001（正向）：独立角色提交/领取/确认任务，重复提交不会产生第二份工作。
2. AC-002（负向）：producer 不能领取或读表，consumer 不能入队，双方不能改 control 或创建表。
3. AC-003（负向）：多个 consumer 同时领取仅一个有效 lease；过期/旧/重复 ACK 被拒绝，三次失败保留 dead job。
4. AC-004（运行时）：pause 停止领取但可完成在途任务；重启保留任务和 receipt；shutdown 给已领取任务有界排空时间。
5. AC-005（负向）：错库、非 verify-full TLS、可公开读取或含覆盖参数的 DSN 被拒绝，错误不泄露凭据。
6. AC-006（资源）：拒绝超预算配置，父 slice 和组件限额一致；live 合成测试验证限流与 OOM 被限制在 QA。
7. AC-007（存储）：仅官方 Hetzner endpoint 的条件更新去掉 ETag 引号；AWS/其他 S3 保持原值，错误 ETag 仍被拒绝，下载无需 SSE-C 请求头。
8. AC-008（回归）：默认 SQS、现有 Bundle 授权和 worker 测试保持通过。没有 Web 契约变化。

9. AC-009（身份）：QA 私有文件身份用于实际对象签名；显式坏文件拒绝，不回退或泄露内容，其他存储仍走原 AWS 身份。
10. AC-010（发布）：PostgreSQL 坐标可通过发布 renderer；混用 SQS、缺 DSN 被拒绝，Hetzner resolver 不调用 CFN。
11. AC-011（主机）：两个颜色使用相同只读身份挂载；缺卷、错数据盘、公开可读或错误凭据路径拒绝；候选准备失败恢复修改前的宿主配置。

## Linked Tests

- `backend/internal/observability/qa/archive/store_hetzner_test.go`::`TestUS062_HetznerConditionalWritesNormalizeOnlyKnownProvider`

- `backend/internal/observability/qa/bundle/queue_postgres_integration_test.go`::`TestUS062_PostgresQueueRoleIsolationLeasesAndSingleConcurrency`
- `backend/internal/observability/qa/bundle/queue_postgres_test.go`::`TestUS062_QueueCredentialRejectsUnsafeConnectionsWithoutLeakingSecrets`
- `backend/internal/observability/qa/bundle/queue_postgres_test.go`::`TestUS062_PostgresQueueSelectionDoesNotLoadAWSCredentials`
- `backend/internal/observability/qa/bundle/queue_postgres_test.go`::`TestUS062_WorkerDrainsClaimedJobAfterShutdownWithBoundedDeadline`
- `backend/internal/config/qa_bundle_env_test.go`::`TestQABundlePostgresConfigRejectsAmbiguousDriver`
- `ops/qa/test_colocation_resources.py`::`QAColocationResourcesTest.test_rejects_overcommit_and_invalid_resource_values`

- `backend/internal/observability/qa/blob_store_creds_test.go`::`TestUS062_BundleSignedDownloadUsesPrivateFileIdentity`
- `backend/internal/config/qa_storage_credentials_test.go`::`TestUS062_QAStoragePrivateCredentialsFailClosed`
- `backend/internal/observability/qa/archive/store_credentials_test.go`::`TestUS062_QAFileIdentityOverridesAmbientAWSOnlyForItsStore`
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_both_colors_receive_readonly_identity_without_secret_contents`
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_root_disk_and_unmounted_data_are_rejected`
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_failed_candidate_restores_environment_before_profile_changes`
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_postgres_renderer_accepts_file_and_rejects_ambiguous_queue`
- `ops/qa/test_resolve_qa_bundle_coordinates.py`::`CoordinatesTest.test_hetzner_manifest_resolves_without_aws_and_rejects_unsafe_values`

- 运行命令:

```bash
python3 -m unittest ops.qa.test_colocation_resources ops.stage0.test_bluegreen_host_runtime ops.qa.test_resolve_qa_bundle_coordinates
cd backend && go test -tags=unit ./internal/config ./internal/observability/qa/archive ./internal/observability/qa/bundle ./internal/observability/qa ./cmd/server
go test -tags=integration ./internal/observability/qa/bundle -run TestUS062 -count=1
```

## Assertions

角色通过真实 PostgreSQL 权限系统验证；并发领取和 receipt fencing 验证状态变化。
资源配置的单元验证不代替线上 cgroup 实测；无真实对象存储/网络验收则不得上线。

## Evidence

会话中的隔离实验和验证结果见 Trace 文档。生产 S3 替换、4 GiB scratch quota、
双向网络拒绝与正式数据迁移尚未验收，不以本 Story 宣称完整 QA 去 AWS。

## Status

- [x] InTest
