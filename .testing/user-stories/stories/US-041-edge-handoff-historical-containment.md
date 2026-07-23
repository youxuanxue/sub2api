# US-041-edge-handoff-historical-containment

- ID: US-041
- Title: Edge handoff 历史凭据暴露面精确处置
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 生产安全运维者**，我希望 **先用只读证据确定每个 Edge 的受影响身份与最小诚实吊销单元，再经人工批准逐目标处置**，**以便** 历史 refresh/session 风险被实际关闭，同时不误伤无关用户且保留可恢复的管理员登录路径。
- Trace:
  - 设计锚点：`docs/approved/p0-conversion-trust.md` §4。
  - Goal：`docs/task-breakdown-p0-conversion-trust-goals.md` P0-G1。
  - 后续协议：US-042 负责新 session-family 标记与 legacy mint 下线。
- Risk Focus:
  - 逻辑错误：把日志未命中解释为从未暴露，或假装能区分实际不可区分的历史 handoff refresh family。
  - 行为回归：吊销 Edge admin 全 session family 后没有可用 break-glass 登录。
  - 安全问题：盘点、日志、终端或 incident artifact 打印 token、mirror key、URL fragment 或 secret-pattern 原值。
  - 运行时：批量轮换中途失败却继续下一目标，造成 prod/Edge key 不一致或管理员锁死。

## Acceptance Criteria

1. **AC-001（只读 inventory）**：Given deployable Edge 清单，When 生成 containment plan，Then 每个目标只包含 Edge/credential/user ID、证据窗口、可区分性、拟吊销单元、影响与 break-glass 状态，不包含任何秘密。
2. **AC-002（诚实范围）**：Given 历史 refresh 无法按 handoff 唯一区分，When 形成计划，Then 明确选择该 Edge 映射 admin user 的完整 refresh/token-version family，并在审批前展示手工会话退出影响，不声称精确到单次 handoff。
3. **AC-003（日志与浏览器边界）**：Given retained 服务日志与操作者选择的本机浏览器审计，When 扫描，Then 输出仅有来源、时间桶、数量和单向相关 digest；浏览器检查/清理必须本机 opt-in，零命中不升级为全历史结论。
4. **AC-004（写操作门禁）**：Given 尚无精确目标清单、恢复路径或人工批准，When 请求 revoke/rotate/cleanup apply，Then fail closed 且不修改生产或本机浏览器状态。
5. **AC-005（逐 Edge 对账）**：Given 一个已批准目标，When 执行处置，Then 先验证 break-glass，再吊销/轮换，并核对 token version、refresh cache、mirror key、手动登录和管理员权限；任一失败立即停止该 Edge。
6. **AC-006（可审计 closeout）**：Given 已处置目标，When 生成 incident record，Then 仅记录 ID、时间窗、数量、原因码、批准人与结果，并证明 artifact secret-pattern 命中为零。

## Assertions

- 默认命令是 plan/read-only；apply 需要精确 Edge ID、确认串和独立人工批准。
- 轮换普通 mirror key 必须发生在其 handoff mint 权限被移除之后。
- 不清理或改写服务日志；浏览器历史删除不是自动化副作用。
- 新协议发布不自动证明历史风险已处置。

## Linked Tests

- `EdgeHandoffContainmentPlanIsReadOnlyAndRedacted` *(planned in the P0-G1 implementation PR)*
- `EdgeHandoffContainmentApplyRequiresExactApprovalAndBreakGlass` *(planned in the P0-G1 implementation PR)*
- `EdgeHandoffContainmentReconciliationStopsOnFirstTargetFailure` *(planned in the P0-G1 implementation PR)*

运行命令：

```bash
python3 -m unittest discover -s ops/observability -p 'test_edge_handoff_containment.py'
```

## Evidence

- 实现 Goal 必须附脱敏 plan、批准记录、逐 Edge 对账和 secret-pattern gate；本 Story 不授权执行生产写操作。

## Status

- Ready — 设计待人工批准；只读工具可准备，生产 apply 保持关闭。
