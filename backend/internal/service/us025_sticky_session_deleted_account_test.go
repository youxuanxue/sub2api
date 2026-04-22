//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newStickyFixtureWithRepo mirrors newStickyFixture but also wires the
// account repo into the SchedulerSnapshotService so the cache-miss fallback
// path is exercised. Used by US-025 deleted-account scenarios where the
// sticky-bound ID is not in the snapshot cache (forcing a repo lookup that
// in production returns ErrAccountNotFound).
func newStickyFixtureWithRepo(t *testing.T, groupID int64, groupPlatform string, pool []*Account, sessionHash string, stickyAccountID int64) *OpenAIGatewayService {
	t.Helper()
	accountsByID := make(map[int64]*Account, len(pool))
	for _, p := range pool {
		if p != nil {
			accountsByID[p.ID] = p
		}
	}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: pool, accountsByID: accountsByID}
	groupRepo := &stubSchedulerGroupRepo{
		groupsByID: map[int64]*Group{
			groupID: {ID: groupID, Platform: groupPlatform},
		},
	}
	repoAccounts := make([]Account, 0, len(pool))
	for _, p := range pool {
		if p != nil {
			repoAccounts = append(repoAccounts, *p)
		}
	}
	repo := stubOpenAIAccountRepo{accounts: repoAccounts}
	snapshotService := &SchedulerSnapshotService{
		cache:       snapshotCache,
		groupRepo:   groupRepo,
		accountRepo: repo,
	}
	bindings := map[string]int64{}
	if sessionHash != "" && stickyAccountID > 0 {
		bindings["openai:"+sessionHash] = stickyAccountID
	}
	cache := &stubGatewayCache{sessionBindings: bindings}
	return &OpenAIGatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                &config.Config{},
		schedulerSnapshot:  snapshotService,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}
}

// US-025: 第五平台 NewAPI 第四轮自检发现的粘性会话清理缺口。
// 当粘性绑定指向的账号被管理员删除（snapshot/DB 都查不到）时，原实现直接
// return nil，未清理 Redis 中的旧映射。后续每次同 sessionHash 请求都会重做
// 一次 NotFound 查询，直到 TTL 到期。
//
// AC-001 (正向自愈): tryStickySessionHit 路径下，binding 指向的账号已被删除时，
//   下一次 SelectAccountWithScheduler 必须把 Redis 中的旧映射清理掉。
//
// AC-002 (Layer-1 sticky 自愈): SelectAccountWithLoadAwareness 路径下同理。
//
// AC-003 (回归): 健康账号的 sticky HIT 行为不受影响——绑定仍生效，没有被
//   误清理。

// stickyAccountKey 复用 newapi_pool 测试已有的 "openai:" 前缀（openAISessionCacheKey
// 内部硬编码），保持与生产 cache 路径一致。
func stickyAccountKey(sessionHash string) string {
	return "openai:" + sessionHash
}

// TestUS025_StickyHit_DeletedAccount_ClearsRedisBinding 覆盖
// tryStickySessionHit 的 err != nil / account == nil 分支。该路径在
// SelectAccountWithLoadAwareness（被 ops_retry 重试链路调用）的非 LoadBatch 分支
// 中触发：legacy selectAccountForModelWithExclusions → tryStickySessionHit。
// 注意：新版 scheduler 的 selectBySessionHash（openai_account_scheduler.go:319-322）
// 已经具备清理逻辑，但 ops_retry 仍走 legacy 路径，因此 tryStickySessionHit 必须
// 也具备等价自愈能力。
func TestUS025_StickyHit_DeletedAccount_ClearsRedisBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(85001)

	// pool 中保留一个健康 newapi 账号作为 fallback；被绑定的 ID 87777 不在
	// pool 也不在 repo —— 模拟"账号刚被管理员删除"的现实场景。
	healthyBackup := newAPIAccount(85101, 5)
	pool := []*Account{healthyBackup}
	deletedAccountID := int64(87777)
	sessionHash := "session-hash-newapi-deleted"

	svc := newStickyFixtureWithRepo(t, groupID, PlatformNewAPI, pool, sessionHash, deletedAccountID)

	// 直接走 SelectAccountWithLoadAwareness，cfg.LoadBatchEnabled 默认 false →
	// selectAccountForModelWithExclusions → tryStickySessionHit。
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err, "fallback to load-balance must succeed when sticky account was deleted")
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, healthyBackup.ID, selection.Account.ID, "must fail over to the surviving newapi backup account")
	require.NotEqual(t, deletedAccountID, selection.Account.ID, "must NOT return the deleted account ID")

	cache, ok := svc.cache.(*stubGatewayCache)
	require.True(t, ok)
	require.GreaterOrEqual(t, cache.deletedSessions[stickyAccountKey(sessionHash)], 1,
		"stale Redis sticky binding must be cleared after the deleted-account miss (US-025 fix)")
}

// TestUS025_LoadAwareLayer1_DeletedAccount_ClearsRedisBinding 覆盖 Layer-1
// sticky 分支（LoadBatchEnabled=true 时进入）。原代码 if err == nil { ... }
// 在 err != nil 时静默跳过，使粘性映射在整个 TTL 内继续指向死 ID。修复后
// err != nil 也必须主动清理。
func TestUS025_LoadAwareLayer1_DeletedAccount_ClearsRedisBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(85003)

	healthyBackup := newAPIAccount(85301, 5)
	pool := []*Account{healthyBackup}
	deletedAccountID := int64(87778)
	sessionHash := "session-hash-newapi-deleted-loadbatch"

	svc := newStickyFixtureWithRepo(t, groupID, PlatformNewAPI, pool, sessionHash, deletedAccountID)
	// 强制走 SelectAccountWithLoadAwareness 的 Layer-1 sticky 分支
	svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, healthyBackup.ID, selection.Account.ID)

	cache, ok := svc.cache.(*stubGatewayCache)
	require.True(t, ok)
	require.GreaterOrEqual(t, cache.deletedSessions[stickyAccountKey(sessionHash)], 1,
		"Layer-1 sticky binding must be cleared when bound account is deleted (US-025 fix)")
}

// TestUS025_StickyHit_HealthyAccount_KeepsRedisBinding 回归保护：当绑定的账号
// 健康存在时，原本的 sticky HIT 行为必须保持——不能把活映射也误删。
func TestUS025_StickyHit_HealthyAccount_KeepsRedisBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(85002)

	healthySticky := newAPIAccount(85201, 7)
	pool := []*Account{healthySticky, newAPIAccount(85202, 5)}
	sessionHash := "session-hash-newapi-healthy"

	svc := newStickyFixtureWithRepo(t, groupID, PlatformNewAPI, pool, sessionHash, healthySticky.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, healthySticky.ID, selection.Account.ID, "healthy sticky binding must continue to HIT")

	cache, ok := svc.cache.(*stubGatewayCache)
	require.True(t, ok)
	require.Equal(t, 0, cache.deletedSessions[stickyAccountKey(sessionHash)],
		"healthy sticky binding must NOT be cleared (regression guard)")
	require.Equal(t, healthySticky.ID, cache.sessionBindings[stickyAccountKey(sessionHash)],
		"binding must still point at the healthy account")
}
