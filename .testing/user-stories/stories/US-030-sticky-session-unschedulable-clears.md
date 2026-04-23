# US-030-sticky-session-unschedulable-clears

- ID: US-030
- Title: tryStickySessionHit 在 bound account 不可调度 / 跨池漂移时主动清理 Redis 绑定
- Version: V1.4.x (hot-fix)
- Priority: P2 (B-7)
- As a / I want / So that:
  作为 **TokenKey 的高 QPS 客户端**，我希望 **当我的 sticky 会话指向的账号被 SetRateLimited / 跨平台漂移时，下一次请求就立即把死绑定清掉并失败转移到健康账号**，**以便** 我不必等 1 小时 sticky TTL 自然过期，期间所有同 sessionHash 的请求也不会反复 cache-hit 死账号再 fall-through 浪费 cache + DB 读。

- Trace:
  - 防御需求轴线：`OpenAIGatewayService.tryStickySessionHit`（legacy 非 LoadBatch sticky 路径）的 `!IsSchedulable() || !IsOpenAICompatPoolMember()` 分支只 `return nil` 不清 Redis；symmetric path `openai_account_scheduler.go::selectBySessionHash` 早就清了。本故事补齐这条不对称。
  - 实体生命周期轴线：sticky binding 生命周期需要"账号失效 → 主动清理 binding"，否则进入"binding 指向死账号"的僵尸状态。
  - 角色 × 能力轴线：`SelectAccountForModelWithExclusions` (legacy 入口) + `SelectAccountWithLoadAwareness` (LoadBatch 入口) 都依赖 tryStickySessionHit；ops_retry 走 legacy 路径所以这条修复对 ops 重试链尤其关键。

- Risk Focus:
  - 逻辑错误：必须 **同时** 在 `!IsSchedulable()` 与 `!IsOpenAICompatPoolMember(groupPlatform)` 两条件分支内清理（任一命中都意味着 binding 该作废）；不能只清一条。
  - 行为回归：健康账号 sticky HIT 行为完全不变（不能误把活映射也删掉）；scheduler 路径行为字节级保持。
  - 安全问题：仅删除自己 group 的 sticky binding，不影响其他 group。
  - 运行时问题：每次 miss 多一次 Redis DEL，但开销远小于反复 cache-hit + filter + fall-through 的总成本。

## Acceptance Criteria

1. **AC-001 (正向 / rate-limited)**：Given 一个 newapi 账号已被 SetRateLimited（resetAt 在未来 1h），sticky binding 指向它，pool 里有健康备用账号，When `SelectAccountWithLoadAwareness` 用同 sessionHash 调用，Then 必然失败转移到健康备用账号，且 `cache.deletedSessions[stickyAccountKey(sessionHash)] >= 1`。
2. **AC-002 (正向 / 跨池漂移)**：Given sticky binding 指向 platform=openai 账号，但 group.platform 已改为 newapi，When 同请求 in 模拟 group.platform=newapi 上下文，Then 必然失败转移到 newapi pool 中的备用账号，且死 binding 被清。
3. **AC-003 (回归 / 健康账号)**：Given sticky binding 指向健康可调度的 newapi 账号，When `SelectAccountWithLoadAwareness` 调用，Then 必须 HIT 健康账号，且 `cache.deletedSessions[stickyAccountKey(sessionHash)] == 0` && binding 仍指向该账号。
4. **AC-004 (回归 / 全量单元测试)**：`go test -tags=unit -count=1 ./internal/service/...` 全绿，特别是已有 US-025 sticky 测试不被本修复破坏。

## Assertions

- `TestUS030_TryStickyHit_AccountUnschedulable_DeletesMapping`: `selection.Account.ID == healthyBackup.ID` && `cache.deletedSessions[...] >= 1`
- `TestUS030_TryStickyHit_AccountWrongPool_DeletesMapping`: `selection.Account.ID == newapiBackup.ID` && `cache.deletedSessions[...] >= 1`
- `TestUS030_TryStickyHit_AccountSchedulable_KeepsMapping_Regression`: `selection.Account.ID == healthy.ID` && `cache.deletedSessions[...] == 0` && `cache.sessionBindings[...] == healthy.ID`

## Linked Tests

- `backend/internal/service/us030_sticky_unschedulable_clears_test.go`::`TestUS030_*` (AC-001 ~ AC-003)
- 既有 `backend/internal/service/us025_sticky_session_deleted_account_test.go` 健康路径仍通过（AC-004 回归保护）

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/service/... -run 'TestUS030|TestUS025'
```

## Evidence

- 修复落地：PR-B (branch `cursor/bug-3f0f-prb`) commit `fe0941ad` — `fix(newapi): tryStickySessionHit clears Redis mapping when bound account unschedulable (Bug B-7)`
- Bug audit 文档：`docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md` § B-7

## Status

- [x] InTest（PR-B 待手工创建 PR；测试已全绿）
