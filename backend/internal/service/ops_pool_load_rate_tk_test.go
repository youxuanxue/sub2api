//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtrLocal(v int) *int { return &v }

func loadInfo(id int64, current, waiting int) *AccountLoadInfo {
	return &AccountLoadInfo{AccountID: id, CurrentConcurrency: current, WaitingCount: waiting}
}

// 核心：池边界 = (platform, group_id, channel_type)。同一 newapi 分组下不同
// channel_type 必须拆成各自的池——deepseek 饱和不能被 volcengine 的空闲平均掉。
func TestAggregatePoolLoads_SplitsNewapiByChannelType(t *testing.T) {
	accounts := []Account{
		// newapi 分组11 deepseek(ch43)：2 账号、各席位5、几乎打满。
		{ID: 1, Platform: PlatformNewAPI, ChannelType: 43, GroupIDs: []int64{11}, Concurrency: 5},
		{ID: 2, Platform: PlatformNewAPI, ChannelType: 43, GroupIDs: []int64{11}, Concurrency: 5},
		// newapi 分组11 volcengine(ch45)：2 账号、各席位5、全空。
		{ID: 3, Platform: PlatformNewAPI, ChannelType: 45, GroupIDs: []int64{11}, Concurrency: 5},
		{ID: 4, Platform: PlatformNewAPI, ChannelType: 45, GroupIDs: []int64{11}, Concurrency: 5},
	}
	loadMap := map[int64]*AccountLoadInfo{
		1: loadInfo(1, 5, 2), // deepseek 满 + 排队
		2: loadInfo(2, 5, 1),
		3: loadInfo(3, 0, 0), // volcengine 空
		4: loadInfo(4, 0, 0),
	}

	pools := aggregatePoolLoads(accounts, loadMap)
	require.Len(t, pools, 2)

	// 已按 LoadRate 降序：deepseek 在前。
	deepseek := pools[0]
	require.Equal(t, 43, deepseek.ChannelType)
	require.Equal(t, 10, deepseek.Seats)                     // 5+5
	require.Equal(t, 13, deepseek.InFlight+deepseek.Waiting) // 10 在途 + 3 排队
	require.InDelta(t, 130.0, deepseek.LoadRatePct, 0.01)

	volc := pools[1]
	require.Equal(t, 45, volc.ChannelType)
	require.InDelta(t, 0.0, volc.LoadRatePct, 0.01)

	// 若错误地把整 newapi/分组11 合成一个池，会得到 (13)/(20)=65% —— 掩盖 deepseek
	// 已 130% 排队的事实。拆开后 max=130%，告警能正确指向 deepseek。
	maxRate, found := maxPoolLoadRate(pools, "", nil)
	require.True(t, found)
	require.InDelta(t, 130.0, maxRate, 0.01)
}

// 单账号池是「单账号触顶」噪音，用户明确不要——排除出池级信号。
func TestAggregatePoolLoads_ExcludesSingleAccountPools(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, GroupIDs: []int64{2}, Concurrency: 4}, // 单账号池
	}
	loadMap := map[int64]*AccountLoadInfo{1: loadInfo(1, 4, 8)} // 200%
	pools := aggregatePoolLoads(accounts, loadMap)
	require.Empty(t, pools, "single-account pool must not produce a pool-level signal")
}

// 无界并发账号（Concurrency<=0 且无 LoadFactor）能无限吸收，整池排除。
func TestAggregatePoolLoads_ExcludesUnboundedPools(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformGemini, GroupIDs: []int64{3}, Concurrency: 0}, // 无界
		{ID: 2, Platform: PlatformGemini, GroupIDs: []int64{3}, Concurrency: 8},
	}
	loadMap := map[int64]*AccountLoadInfo{1: loadInfo(1, 100, 0), 2: loadInfo(2, 8, 4)}
	pools := aggregatePoolLoads(accounts, loadMap)
	require.Empty(t, pools, "pool with an unbounded member can never saturate on concurrency")
}

// LoadFactor 覆盖 Concurrency 作为席位上限。
func TestAccountConcurrencyCap_LoadFactorWins(t *testing.T) {
	cap, bounded := accountConcurrencyCap(&Account{LoadFactor: intPtrLocal(3), Concurrency: 9})
	require.True(t, bounded)
	require.Equal(t, 3, cap)

	cap, bounded = accountConcurrencyCap(&Account{Concurrency: 9})
	require.True(t, bounded)
	require.Equal(t, 9, cap)

	_, bounded = accountConcurrencyCap(&Account{})
	require.False(t, bounded)
}

// 账号在多个分组 → 每个分组各自成池，各计该账号的席位与负载。
func TestAggregatePoolLoads_MultiGroupAccountCountsPerGroup(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformAnthropic, GroupIDs: []int64{1, 2}, Concurrency: 4},
		{ID: 2, Platform: PlatformAnthropic, GroupIDs: []int64{1, 2}, Concurrency: 4},
	}
	loadMap := map[int64]*AccountLoadInfo{1: loadInfo(1, 4, 0), 2: loadInfo(2, 2, 0)}
	pools := aggregatePoolLoads(accounts, loadMap)
	require.Len(t, pools, 2, "one pool per (platform, group)")
	for _, p := range pools {
		require.Equal(t, 2, p.Accounts)
		require.Equal(t, 8, p.Seats)
		require.Equal(t, 6, p.InFlight) // 4+2
		require.InDelta(t, 75.0, p.LoadRatePct, 0.01)
	}
}

// scope 过滤：platform/group_id 限定；channel_type 永不进 scope。
func TestMaxPoolLoadRate_ScopeFilter(t *testing.T) {
	pools := []PoolLoad{
		{Platform: PlatformNewAPI, GroupID: 11, ChannelType: 43, LoadRatePct: 130},
		{Platform: PlatformOpenAI, GroupID: 2, LoadRatePct: 95},
	}
	// 限定 openai → 只看 95，不被 newapi 130 影响。
	rate, found := maxPoolLoadRate(pools, PlatformOpenAI, nil)
	require.True(t, found)
	require.InDelta(t, 95.0, rate, 0.01)

	// 限定一个无池的 group → found=false。
	gid := int64(999)
	_, found = maxPoolLoadRate(pools, "", &gid)
	require.False(t, found)
}

// 卡片「主因」只展开越过阈值的最饱和池，并标注「排队中」。
func TestFormatPoolLoadCause_OverThresholdOnly(t *testing.T) {
	pools := []PoolLoad{
		{Platform: PlatformNewAPI, GroupID: 11, ChannelType: 43, Accounts: 3, Seats: 12, InFlight: 12, Waiting: 4, LoadRatePct: 133},
		{Platform: PlatformOpenAI, GroupID: 2, Accounts: 2, Seats: 16, InFlight: 8, Waiting: 0, LoadRatePct: 50},
	}
	rule := &OpsAlertRule{MetricType: "pool_load_rate", Operator: ">=", Threshold: 90}
	got := formatPoolLoadCause(pools, rule, "", nil)
	require.Contains(t, got, "DeepSeek")  // channel 名而非裸渠道号
	require.Contains(t, got, "排队中")       // Waiting>0
	require.NotContains(t, got, "openai") // 50% 未越阈值，不展开
}
