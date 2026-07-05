//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerSnapshotOutboxReplay(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	client := testEntClient(t)

	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")

	accountRepo := newAccountRepositoryWithSQL(client, integrationDB, nil, nil)
	outboxRepo := NewSchedulerOutboxRepository(integrationDB)
	cache := NewSchedulerCache(rdb)

	cfg := &config.Config{
		RunMode: config.RunModeStandard,
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxPollIntervalSeconds:  1,
				FullRebuildIntervalSeconds: 0,
				DbFallbackEnabled:          true,
			},
		},
	}

	account := &service.Account{
		Name:        "outbox-replay-" + time.Now().Format("150405.000000"),
		Platform:    domain.PlatformOpenAI,
		Type:        domain.AccountTypeAPIKey,
		Status:      domain.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    1,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}
	require.NoError(t, accountRepo.Create(ctx, account))
	require.NoError(t, cache.SetAccount(ctx, account))

	svc := service.NewSchedulerSnapshotService(cache, outboxRepo, accountRepo, nil, cfg)
	svc.Start()
	t.Cleanup(svc.Stop)

	// 该断言验证的是 outbox 重放把 last_used 传播进【调度热读快照】（sched:meta
	// 键，GetSnapshot 读取的就是它）。ungrouped openai 账号属于默认桶
	// {GroupID:0, openai, single}。
	//
	// 注意：不能像旧版那样断言 cache.GetAccount（完整账号键 sched:acc）——
	// 自 #1723 起 outbox 的 account_last_used 路径只刷 sched:meta，完整键由快照
	// 重建/账号变更刷新。若仍断言完整键，本测试会改为被 startup rebuild 的副作用
	// 兜过（名实不符、对 outbox 回归零覆盖）。
	bucket := service.SchedulerBucket{GroupID: 0, Platform: domain.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	snapshotHasAccount := func() (*service.Account, bool) {
		snap, hit, err := cache.GetSnapshot(ctx, bucket)
		if err != nil || !hit {
			return nil, false
		}
		for _, a := range snap {
			if a != nil && a.ID == account.ID {
				return a, true
			}
		}
		return nil, false
	}

	// 先等 startup rebuild 把账号灌入快照（此时 LastUsedAt 仍为 nil）。rebuild
	// 只在启动时跑一次（FullRebuildIntervalSeconds=0 已禁用周期重建），过此 gate
	// 之后唯一能更新快照 meta 的路径就是 outbox 重放本身。
	require.Eventually(t, func() bool {
		_, ok := snapshotHasAccount()
		return ok
	}, 5*time.Second, 100*time.Millisecond, "startup rebuild 应先把账号写入 openai/0/single 快照")

	require.NoError(t, accountRepo.UpdateLastUsed(ctx, account.ID))
	updated, err := accountRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.LastUsedAt)
	expectedUnix := updated.LastUsedAt.Unix()

	require.Eventually(t, func() bool {
		a, ok := snapshotHasAccount()
		if !ok || a.LastUsedAt == nil {
			return false
		}
		return a.LastUsedAt.Unix() == expectedUnix
	}, 5*time.Second, 100*time.Millisecond)
}
