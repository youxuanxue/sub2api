//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Tests for upstream Wei-Shaw/sub2api#2859 — sticky slot-full escape.
// See docs/approved/sticky-routing.md §11.5.
//
// 行为表（与开关无关的 happy path + 三个分支）：
//   - sticky 账号有空槽            → 用 sticky（不变）
//   - sticky 满、池里有空、逃逸 ON → 逃逸到空账号（修复点）
//   - sticky 满、全池也满、逃逸 ON → 回到 sticky WaitPlan（缓存仍热）
//   - sticky 满、池里有空、逃逸 OFF → 在 sticky 上排队（今日行为）

// slotEscapeSettingRepo（escape 开关 settingRepo 桩）定义在无 build-tag 的
// openai_account_scheduler_test.go 中，以便 no-tag / unit / integration 三种
// 构建下都可见——该 stub 被无 tag 的 SessionStickyBusyKeepsSticky 测试复用。

// newSlotEscapeFixture 构建一个 OpenAIGatewayService，可控制每账号并发槽获取
// 结果（acquireResults）与可选 settingService（驱动 escape 开关）。
func newSlotEscapeFixture(
	t *testing.T,
	groupID int64,
	pool []*Account,
	sessionHash string,
	stickyAccountID int64,
	acquireResults map[int64]bool,
	settingService *SettingService,
) *OpenAIGatewayService {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	accountsByID := make(map[int64]*Account, len(pool))
	repoAccounts := make([]Account, 0, len(pool))
	for _, p := range pool {
		if p != nil {
			accountsByID[p.ID] = p
			repoAccounts = append(repoAccounts, *p)
		}
	}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: pool, accountsByID: accountsByID, filterPlatform: PlatformOpenAI}
	groupRepo := &stubSchedulerGroupRepo{groupsByID: map[int64]*Group{groupID: {ID: groupID, Platform: PlatformOpenAI}}}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache, groupRepo: groupRepo}

	bindings := map[string]int64{}
	if sessionHash != "" && stickyAccountID > 0 {
		bindings["openai:"+sessionHash] = stickyAccountID
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Gateway.Scheduling.LoadBatchEnabled = false

	return &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: repoAccounts},
		cache:              &stubGatewayCache{sessionBindings: bindings},
		cfg:                cfg,
		schedulerSnapshot:  snapshotService,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{acquireResults: acquireResults}),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		settingService:     settingService,
	}
}

// A — 修复点：sticky 账号槽满、池里有空账号、逃逸默认开（settingService=nil → fail-open true）
// → 逃逸到空账号，而不是把用户困在堵塞的 sticky 账号上排队。
func TestUS2859_SlotFullEscape_EscapesToFreePoolAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(85001)
	sticky := openAIAccount(1, 7)
	free := openAIAccount(2, 5)
	svc := newSlotEscapeFixture(t, groupID, []*Account{sticky, free}, "sess-escape", 1,
		map[int64]bool{1: false, 2: true}, nil)

	sel, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess-escape", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel)
	require.NotNil(t, sel.Account)
	require.Equal(t, int64(2), sel.Account.ID, "槽满应逃逸到池里的空账号，而非困在 sticky 账号")
	require.True(t, sel.Acquired, "逃逸的账号应真正拿到槽")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

// B — happy path 不变：sticky 账号有空槽 → 仍走 sticky（缓存最优）。
func TestUS2859_SlotFullEscape_StickyHasSlot_StaysSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(85002)
	sticky := openAIAccount(1, 7)
	free := openAIAccount(2, 5)
	svc := newSlotEscapeFixture(t, groupID, []*Account{sticky, free}, "sess-happy", 1,
		map[int64]bool{1: true, 2: true}, nil)

	sel, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess-happy", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel)
	require.NotNil(t, sel.Account)
	require.Equal(t, int64(1), sel.Account.ID, "sticky 有空槽时必须走 sticky")
	require.True(t, sel.Acquired)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
}

// C — 全池也满、逃逸 ON → 回到 sticky 的 WaitPlan（在缓存最热的原账号上等，不比今天差）。
func TestUS2859_SlotFullEscape_PoolAlsoFull_FallsBackToStickyWait(t *testing.T) {
	ctx := context.Background()
	groupID := int64(85003)
	sticky := openAIAccount(1, 7)
	other := openAIAccount(2, 5)
	svc := newSlotEscapeFixture(t, groupID, []*Account{sticky, other}, "sess-allfull", 1,
		map[int64]bool{1: false, 2: false}, nil)

	sel, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess-allfull", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel)
	require.NotNil(t, sel.Account)
	require.Equal(t, int64(1), sel.Account.ID, "全池满时应回到 sticky 账号等待（缓存仍热）")
	require.False(t, sel.Acquired, "全满时返回的是 WaitPlan，未真正拿到槽")
	require.NotNil(t, sel.WaitPlan)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
}

// E — 开关关闭 → 退回今日行为：sticky 槽满即在原账号排队，即使池里有空账号也不逃逸。
func TestUS2859_SlotFullEscape_Disabled_QueuesOnSticky(t *testing.T) {
	ctx := context.Background()
	// 重置进程内缓存，确保读到本测试 settingRepo 的值。
	stickySlotFullEscapeCache.Store(&stickySlotFullEscapeCacheEntry{expiresAt: 0})
	t.Cleanup(func() { stickySlotFullEscapeCache.Store(&stickySlotFullEscapeCacheEntry{expiresAt: 0}) })

	groupID := int64(85004)
	sticky := openAIAccount(1, 7)
	free := openAIAccount(2, 5)
	ss := &SettingService{settingRepo: &slotEscapeSettingRepo{val: "false"}}
	svc := newSlotEscapeFixture(t, groupID, []*Account{sticky, free}, "sess-off", 1,
		map[int64]bool{1: false, 2: true}, ss)

	sel, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess-off", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel)
	require.NotNil(t, sel.Account)
	require.Equal(t, int64(1), sel.Account.ID, "逃逸关闭时即使池里有空账号也必须留在 sticky 账号排队")
	require.False(t, sel.Acquired)
	require.NotNil(t, sel.WaitPlan)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
}

// D — reader：默认开（未设置/空/error/"true"），仅显式 "false" 关闭。
func TestIsStickySlotFullEscapeEnabled_Default(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"unset/empty defaults true", "", true},
		{"explicit true", "true", true},
		{"explicit false", "false", false},
		{"garbage defaults true (opt-out)", "yes", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stickySlotFullEscapeCache.Store(&stickySlotFullEscapeCacheEntry{expiresAt: 0})
			ss := &SettingService{settingRepo: &slotEscapeSettingRepo{val: tc.val}}
			require.Equal(t, tc.want, ss.IsStickySlotFullEscapeEnabled(context.Background()))
		})
	}

	// nil SettingService / nil repo → fail-open true。
	stickySlotFullEscapeCache.Store(&stickySlotFullEscapeCacheEntry{expiresAt: 0})
	var nilSvc *SettingService
	require.True(t, nilSvc.IsStickySlotFullEscapeEnabled(context.Background()))
}
