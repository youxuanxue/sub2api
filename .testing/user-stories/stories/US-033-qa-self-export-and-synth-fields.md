# US-033 QA self-export endpoint + synth_* fields

- ID: US-033
- Title: 用户自助导出自己的 qa_records（issue #59 双 Gap 修复）
- Version: V1.0
- Priority: P0
- As a / I want / So that:
  作为持有 user-scope token 的开发者（M0 dual-CC pipeline 即首个调用方），
  我希望 `POST /api/v1/users/me/qa/export` 能按 `synth_session_id` 拉到我自己
  的 qa_records 打包 zip，以便 `traj_builder` 把会话拼成 traj.jsonl，把
  `make m0` 的 verify.sh 五项机械门跑出 verdict。
- Trace:
  - 角色 × 能力（普通用户 × 自助导出捕获数据）
  - 防御需求（不得越权读取他人 qa_records）
  - 实体生命周期（qa_records 写入 → 用户取出 → S3 zip 24h 过期）
- Risk Focus:
  - 逻辑错误：`synth_session_id` 过滤未叠加 `user_id` → 跨用户读取；
    时间窗 fallback 缺失 → 用户一键导出全量历史；
    handler 未鉴权 → 匿名 POST 触发 DB/S3 流量
  - 行为回归：`Service.ExportUserData` 在 origin/main 上没有任何调用方
    （只是写好但未挂路由的死代码），所以本 PR 直接把它改成
    `ExportUserData(ctx, userID, ExportFilter)`，没有"老签名"需要兼容；
    `ops_xx.md §2 "100% QA Capture"` 中 capture 侧的写入字段不能减；
    新增字段全部 nullable / 有默认 → 老在线流量不受影响
  - 安全问题：path traversal 不适用（无路径参数）；越权访问通过
    服务层 `qarecord.UserIDEQ(subject.UserID)` 兜底；header 长度封顶 256B
    防止恶意 `synth_session_id` 撑爆行
  - 运行时问题：QA capture 在某环境关闭时端点必须返回 503 而非 500（M0 客户端
    通过状态码判断"环境不就绪 → 跳过"，500 会被当作 bug 上报）

## Acceptance Criteria

1. **AC-001 (正向 / Gap 1)**：Given QA capture 已启用，user 7 名下有
   `synth_session_id="m0-XYZ"` 的 1 条 qa_record，When user 7 的 JWT 调
   `POST /api/v1/users/me/qa/export {"synth_session_id":"m0-XYZ"}`，Then
   返回 200 + `{download_url, expires_at, record_count: 1}`，下载 URL 是
   24h 过期的 presigned URL（localfs 模式下为 `file://`）。

2. **AC-002 (正向 / 默认窗口)**：Given user 9 名下有 1 小时前 1 条 + 48 小时前 1 条
   非 synth-tagged 记录，When user 9 调 `POST /api/v1/users/me/qa/export`（空 body），
   Then 返回 200 且 `record_count == 1`（默认窗口 24h，过期记录不计入）。

3. **AC-003 (负向 / 鉴权)**：Given 没有 JWT auth subject，When 匿名 POST
   `/api/v1/users/me/qa/export`，Then 返回 401 且 service 层不被调用
   （DB/S3 零流量）。

4. **AC-004 (负向 / 越权)**：Given 受害者 user 200 名下有 `synth_session_id="m0-V"` 的
   1 条记录，攻击者 user 100 名下无任何记录，When user 100 用自己的 JWT 调
   `POST /api/v1/users/me/qa/export {"synth_session_id":"m0-V"}`，Then 返回 200
   且 `record_count == 0`（服务层 `WHERE user_id = 100` 兜底，不论攻击者怎么
   猜 session id 都无法读到他人数据）。

5. **AC-005 (Gap 2 / 字段持久化)**：Given gateway 收到带 `X-Synth-Session: m0-A` 的
   `/v1/messages` 请求，When 中间件 capture 完成写入 qa_records，Then
   该行的 `synth_session_id` / `synth_role` / `synth_engineer_level` /
   `dialog_synth` 字段被填充（M0 客户端在 export 出来的 jsonl 中能直接读到）。

6. **AC-006 (回归)**：Given 在线流量从未发送 `X-Synth-*` 头，When 写入 qa_records，
   Then 4 个新字段保持 NULL / false 默认值，对原有 `request_id`/`user_id`/
   `api_key_id`/`upstream_model` 等字段编码无任何影响（与 `ops_xx.md §2` 兼容
   性规则一致："API changes must not break existing online callers"）。

7. **AC-007 (单一入口)**：Given 没有现存调用方，When 用 `ExportFilter{Since, Until}`
   按时间窗导出，Then 与按 session id 导出走完全相同的代码路径（一个 SQL
   构建器、一段 zip 打包逻辑、一份 ExportResult），不出现 GDPR 与 M0 两条平行
   分支（Jobs：one canonical path per intent）。

## Assertions

- HTTP 状态码：401（无 auth）、503（service disabled）、400（坏 JSON）、
  200（正常）、200 + record_count=0（越权失败）。
- Response envelope 形态：`{code:0, message:"success", data:{download_url,
  expires_at, record_count}}`，`expires_at` 为 24h 后的 UTC RFC3339 时间。
- DB 行为：导出 SQL 始终带 `user_id = ?`；`synth_session_id` 设置时
  覆盖时间窗（M0 session 可能跨默认 24h）。
- 字段完整性：导出 zip 内 `qa_records.jsonl` 单行记录包含
  `synth_session_id`、`synth_role`（与 ent JSON tag 一致），M0 端
  `verify_c2_keys.py` 读 `api_key_id`、`verify_c3_model.py` 读
  `upstream_model` 不需要适配。

## Linked Tests

- `backend/internal/observability/qa/service_export_test.go`::`TestUS059_ExportUserData_OnlyOwnRecords`
- `backend/internal/observability/qa/service_export_test.go`::`TestUS059_ExportUserData_BySynthSessionID`
- `backend/internal/observability/qa/service_export_test.go`::`TestUS059_ExportUserData_RoleNarrows`
- `backend/internal/observability/qa/service_export_test.go`::`TestUS059_ExportUserData_TimeWindowOnly`
- `backend/internal/observability/qa/service_export_test.go`::`TestUS059_ExportUserData_UnknownSession_EmptyNotError`
- `backend/internal/observability/qa/middleware_synth_test.go`::`TestUS059_CaptureSynthHeaders_AllPresent`
- `backend/internal/observability/qa/middleware_synth_test.go`::`TestUS059_CaptureSynthHeaders_PipelineAloneFlipsDialogSynth`
- `backend/internal/observability/qa/middleware_synth_test.go`::`TestUS059_CaptureSynthHeaders_AbsentReturnsEmpty`
- `backend/internal/observability/qa/middleware_synth_test.go`::`TestUS059_CaptureSynthHeaders_BoundedLength`
- `backend/internal/handler/qa_handler_test.go`::`TestUS059_ExportSelf_Unauthenticated_401`
- `backend/internal/handler/qa_handler_test.go`::`TestUS059_ExportSelf_DisabledService_503`
- `backend/internal/handler/qa_handler_test.go`::`TestUS059_ExportSelf_BySynthSessionID_200`
- `backend/internal/handler/qa_handler_test.go`::`TestUS059_ExportSelf_DefaultsTo24hWindow`
- `backend/internal/handler/qa_handler_test.go`::`TestUS059_ExportSelf_BadRequest_InvalidJSON`
- `backend/internal/handler/qa_handler_test.go`::`TestUS059_ExportSelf_CannotEscapeUserScope`
- 运行命令：`cd backend && go test -tags=unit -timeout 120s ./internal/observability/qa/... ./internal/handler/...`

## Evidence

- `.testing/user-stories/attachments/us033-test-output.txt`（待 PR CI 落产物）

## Status

- [x] InTest — backend 15 个 unit test（service 5 + middleware 4 + handler 6）
  全绿；schema + migration 已落盘；wire 已重生成；M0 端到端待 traj 仓库
  联动验证后翻 Done。
