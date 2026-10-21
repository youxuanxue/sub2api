# US-070-kiro-file-tool-chunking

- ID: US-070
- Priority: P1
- Title: Kiro file tool chunking is account opt-in and preserves client turns
- As a / I want / So that: 作为运维，我希望仅为已确认的 Kiro 账号启用分块提示，以缓解大文件工具静默断流，同时保留客户工具循环和真实错误。
- Trace: `docs/approved/kiro-file-tool-chunking.md`；用户于本次对话确认原型方案。
- Risk Focus:
  - 逻辑错误：非布尔开关、错误平台或非文件工具被意外启用。
  - 行为回归：CC 过滤删除提示、工具名/参数改变、历史失败结果被改写、缓存前缀不稳定。
  - 安全问题：客户端请求不能替代账号策略，管理员改 Extra 不应附带凭据/优先级写入；沿用既有管理员认证，无新增权限入口。
  - 运行时问题：结束后隐式续跑、已输出后重试放大；分块提示不是强约束，模型仍可超限。

## Acceptance Criteria

1. AC-001（正向）：账号布尔 true 且声明文件工具时，过滤后只增加一份稳定提示；原始请求、当前消息、工具 schema 和历史保持语义。
2. AC-002（负向）：缺失/false/错误类型、非 Kiro 或非文件工具不启用；默认转换与原行为一致。
3. AC-003（回归）：JSON/SSE 返回原始工具 ID、参数和客户端声明的工具名；正常 END_TURN 结束，流中断发出错误且不重放。
4. AC-004（配置）：既有管理员批量 Extra 更新可按显式账号 ID 开启/关闭，仅补丁这一键，不改 priority/credentials。

## Assertions

- AC-001 → TestUS070_FileToolGuidanceScope / TestUS070_FileToolGuidancePreservesHistory：精确比较 payload、历史、conversation ID 和未修改的输入。
- AC-002 → TestUS070_FileToolChunkingAccountOptIn / TestUS070_FileToolGuidanceScope：不同类型/平台/工具范围断言。
- AC-003 → TestUS070_FileToolChunkingForward / TestUS070_FileToolChunkingDoesNotContinueOrReplay：解析 JSON/SSE 工具对象；断言一次上游调用及正确终止。
- AC-004 → TestUS070_FileToolChunkingAdminPatch：实际 service 入口传往 repository 的增量、目标与无关字段。

## Linked Tests

- `backend/internal/integration/kiro/file_tool_chunking_test.go`::`TestUS070_FileToolGuidanceScope`
- `backend/internal/integration/kiro/file_tool_chunking_test.go`::`TestUS070_FileToolGuidancePreservesHistory`
- `backend/internal/service/kiro_file_tool_chunking_test.go`::`TestUS070_FileToolChunkingAccountOptIn`
- `backend/internal/service/kiro_file_tool_chunking_test.go`::`TestUS070_FileToolChunkingForward`
- `backend/internal/service/kiro_file_tool_chunking_test.go`::`TestUS070_FileToolChunkingDoesNotContinueOrReplay`
- `backend/internal/service/kiro_file_tool_chunking_test.go`::`TestUS070_FileToolChunkingAdminPatch`
- 运行命令：`cd backend && go test -tags=unit ./internal/integration/kiro ./internal/service -run '^TestUS070_' -count=1`

## Evidence

正式实现的测试与反向禁用 Forward 集成的失败对照；真实原型结果见 `docs/kiro-open-source-validation-20261021.md`。600 行任务完成、恢复保留前文、raw 参数对照一致；模型曾超出建议上限，不能称为根治。API/客户端工具实测不是 UI e2e，无新 UI 工件。

## Status

- [x] InTest — 正式代码与本地测试；未部署或启用生产账号。
