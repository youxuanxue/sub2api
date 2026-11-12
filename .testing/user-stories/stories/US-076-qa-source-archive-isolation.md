# US-076-qa-source-archive-isolation

- ID: US-076
- Priority: P0
- Title: 双应用节点 QA 归档隔离与全站清理保护
- As a / I want / So that: 作为生产运维，我要将两应用节点同小时的文件与回执分开保存，从而不覆盖彼此或用一个节点的成功证明全站可删。
- Trace: `docs/approved/design-prod-app-parallel-replacement.md` 的 QA 集中管理方案；源生命周期、重试与删除防御。
- Risk Focus:
  - 逻辑错误：站点、节点与 UTC 小时绑定，节点身份不得穿越目录。
  - 行为回归：旧 v1 归档和 installer 默认行为不变。
  - 安全问题：跨节点回执、错误摘要或单源回执不能放行全站删除。
  - 运行时问题：迟到文件拒绝静默跳过，发布中断只恢复内容一致的快照，重复执行幂等。

## Acceptance Criteria

1. AC-001 (正向): Given 两节点同站点同小时同文件名 When 归档 Then 目标、摘要与回执隔离，原始内容与 hash 各自保留。
2. AC-002 (负向): Given 不安全节点身份、非 UTC 整点或错节点回执 When 归档 Then 拒绝，且不发送文件或覆盖既有数据。
3. AC-003 (正向/负向): Given 已发布节点小时 When 重试 Then 内容一致幂等；新增或变化文件报错并保留源，缺回执只恢复相同快照。
4. AC-004 (负向): Given 单节点成功回执 When 全站 cleanup 检查 Then 不放行，包括回执误放到旧路径或被误标 v1。
5. AC-005 (回归): Given 不传节点身份的旧归档任务 When 运行 Then v1 目标和回执保持原契约，旧测试通过。

## Assertions

断言两份实际文件内容、manifest 摘要、重复归档结果、错误后的源保留与 receipt 不变，
以及 Go cleanup 的拒绝结果；不把目录存在当作验收。
source v2 只证明文件快照，不证明 capture 封口、全站完整性或线上迁移完成。

## Linked Tests

- `ops/qa/test_node1_archive.py`::`SourceArchiveTest.test_two_sources_cannot_overwrite_each_other_or_legacy` (AC-001, AC-005)
- `ops/qa/test_node1_archive.py`::`SourceArchiveTest.test_run_isolates_source_summary_and_preserves_legacy_summary` (AC-001)
- `ops/qa/test_node1_archive.py`::`SourceArchiveTest.test_unsafe_identity_and_non_utc_hour_fail_before_copy` (AC-002)
- `ops/qa/test_node1_archive.py`::`SourceArchiveTest.test_receipt_from_another_source_or_legacy_cannot_authorize_a_source` (AC-002)
- `ops/qa/test_node1_archive.py`::`SourceArchiveTest.test_late_source_files_fail_without_replacing_archive_or_receipt` (AC-003)
- `ops/qa/test_node1_archive.py`::`SourceArchiveTest.test_interrupted_publish_recovers_only_the_matching_source_snapshot` (AC-003)
- `ops/qa/test_node1_remote.py`::`SourceRemoteArchiveTest.test_sources_use_distinct_remote_targets_and_idempotent_receipts` (AC-001, AC-003)
- `ops/qa/test_node1_remote.py`::`SourceRemoteArchiveTest.test_wrong_source_receipt_is_rejected_before_contacting_node1` (AC-002)
- `backend/internal/observability/qa/lifecycle/node1_receipt_test.go`::`TestSourceReceiptCannotAuthorizeSiteCleanup` (AC-004)
- `ops/qa/test_node1_archive.py`::`Node1ArchiveTest.test_copy_writes_manifest_and_atomic_receipt` (AC-005)
- Run: `python3 -m unittest ops.qa.test_node1_archive ops.qa.test_node1_remote`
- Run: `cd backend && go test -tags=unit ./internal/observability/qa/lifecycle ./cmd/qa-local-maintenance`
- Run: `make -C backend test-qa-local`

## Evidence

本地单元与真实 PostgreSQL 集成结果按实际执行记录。无 UI 变更，不要求 Playwright。
2026-11-11 本地 archive/remote、Go lifecycle 和真实 PostgreSQL host lifecycle 测试通过。
完整 `make test-qa-local` 在临时 shell wrapper 内显式设置
`DYLD_LIBRARY_PATH=/opt/homebrew/lib` 后通过，包括 layout 修复集成测试；
这是 macOS 系统 shell 丢弃继承变量的本机适配，未修改生产 decoder。
集中调度、全站参与源集合、封口与逐节点文件清理未在本 Story 内宣称完成；
现有 installer 未自动启用 source 模式，线上 owner、timer、数据与入口均未改。

## Status

- InTest
