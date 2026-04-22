# US-025-newapi-sticky-session-deleted-account-cleanup

- ID: US-025
- Title: NewAPI 第五平台 round-4 audit 修复（粘性会话指向已删除账号时主动清理 Redis 绑定）
- Version: V1.0
- Priority: P1
- As a / I want / So that: 作为运维，我希望当一个粘性会话绑定的账号被
  admin 删除（或在快照之外被标记不可用）时，Redis 中的 sticky 映射不再
  停留 TTL 的整个生命周期，以便后续相同 sessionHash 的请求不再每次都重
  做一次 `ErrAccountNotFound` 查询并降级到 Layer 2 选择，而是立刻通过
  Layer 2 重新写一条新的健康映射，自愈完成。这条修复对 OpenAI 兼容池
  的两个真实成员（`openai` + 第五平台 `newapi`）共同生效。
- Trace: 防御需求（粘性会话生命周期 × 账号删除事件）
  + 实体生命周期（Account → 删除态 → 粘性映射应跟随失效）
- Risk Focus:
  - 逻辑错误：`tryStickySessionHit` 与 `SelectAccountWithLoadAwareness`
    Layer-1 sticky 块在 `getSchedulableAccount` 返回 `ErrAccountNotFound`
    或 nil account 时直接 `return nil`，没有调用
    `deleteStickySessionAccountID` 清理 Redis 中的失效映射；
    OpenAI compat 调度池里 newapi 与 openai 共用这一条粘性路径，删除
    一个 newapi 账号会让该 sessionHash 在 sticky TTL 内每个请求都先 hit
    一次 NotFound 再 fallback——既浪费一次仓储调用又拉低请求 P95。
  - 行为回归：粘性会话的"健康账号继续命中"不能受影响（仅在删除/不存在
    路径上清理）；Layer 2 选择成功后会重新写入新映射，但若池为空，仍
    需返回明确错误，不能因为新映射写不进就把旧失效映射保留。
  - 安全问题：不适用——本次修复只在已认证的内部调度路径上做 Redis 写
    清理，不放宽任何外部可见的访问控制。
- Round-1 (US-022) 修了 admin 平面 5 项缺口；round-2 (US-023) 修了
  runtime 3 项缺口；round-3 (US-024) 修了批量导入 + 卡片过滤；round-4
  从粘性会话生命周期的"账号删除事件"角度补齐 self-heal 路径，进一步
  缩小"newapi/openai 共享调度池路径与四平台路径的对称性差距"。

## Acceptance Criteria

1. AC-001 (正向 / sticky-hit-path 自愈): Given 一个 newapi 账号在
   sessionHash → accountID 映射写入后被 admin 删除，When 同样
   sessionHash 的请求经过 `SelectAccountWithLoadAwareness` →
   `selectAccountForModelWithExclusions` → `tryStickySessionHit`，Then
   该函数必须调用 `deleteStickySessionAccountID(groupID, sessionHash)`
   把 Redis 里的失效映射删掉，并返回 `nil` 让外层 fall back 到 Layer 2
   load-aware 选择。
2. AC-002 (正向 / load-aware-layer1 自愈): Given 同样的删除场景但部署
   开启了 `cfg.Gateway.Scheduling.LoadBatchEnabled`（走
   `SelectAccountWithLoadAwareness` 的 Layer-1 sticky 分支而非
   `tryStickySessionHit`），When 进入该 Layer-1 块，Then 同样必须在
   `getSchedulableAccount` 失败/nil 路径上调用
   `deleteStickySessionAccountID`；两个 sticky 路径行为对称。
3. AC-003 (回归保护 / 健康映射不被误删): Given 粘性映射指向的账号仍
   存在且可调度，When 进入 `tryStickySessionHit`，Then `deleteSticky
   SessionAccountID` 不能被调用，原映射保持不变（避免修复溢出到正常
   路径上把所有 sticky 命中都清掉一遍）。
4. AC-004 (回归): Given 本次代码变更，When 执行
   `go test -tags=unit -run 'TestUS025_' ./backend/internal/service/...`，
   Then 全部通过。

## Assertions

- `redis.Get(stickyKey) == redis.Nil`（AC-001、AC-002）
- 返回的健康账号 `account.Platform == "newapi"` 且 `account.ID !=
  deletedAccountID`（AC-001、AC-002）
- `redis.Get(stickyKey) == healthyAccountID` 且 deleteSticky 调用次数为
  0（AC-003，通过 spy 计数验证）
- `go test -tags=unit -v -run 'TestUS025_' ./backend/internal/service/...`
  exit 0（AC-004）
- 反向验证：临时回退两处 `deleteStickySessionAccountID` 调用，AC-001 与
  AC-002 必须 fail（确保测试真的在覆盖修复点而非其它路径）

## Linked Tests

- `backend/internal/service/us025_sticky_session_deleted_account_test.go`::`TestUS025_StickyHit_DeletedAccount_ClearsRedisBinding` (AC-001)
- `backend/internal/service/us025_sticky_session_deleted_account_test.go`::`TestUS025_LoadAwareLayer1_DeletedAccount_ClearsRedisBinding` (AC-002)
- `backend/internal/service/us025_sticky_session_deleted_account_test.go`::`TestUS025_StickyHit_HealthyAccount_KeepsRedisBinding` (AC-003)
- 运行命令: `go test -tags=unit -v -run 'TestUS025_' ./backend/internal/service/...`

## Evidence

- 全部 3 个单测通过（已本地验证）
- 修改的代码位置：
  - `backend/internal/service/openai_gateway_service.go::tryStickySessionHit`
    错误路径上调用 `deleteStickySessionAccountID`
  - `backend/internal/service/openai_gateway_service.go::SelectAccountWithLoadAwareness`
    Layer-1 sticky 块的相同失败路径上调用同名清理

## Out of Scope (Round-4 audit findings 但本次不修)

- `error_passthrough_rule.AllPlatforms()`/`channel_service.go` 等若干
  平台列表注释漏写 newapi：本轮顺手补齐了，但属于纯文案/常量同步，
  不进入 AC（无法机械断言行为）。
- TLS 指纹（`DoWithTLS`）只对 Anthropic 启用：`frontend/types` 已显式
  标注 Anthropic-only，是设计选择，不算 newapi 缺口。
- 前端 4 平台模型 fallback 字段：与 `setting_service.GetFallbackModel`
  实际只服务 Antigravity 一致，无 newapi 行为缺失。

## Status

- [x] Done
