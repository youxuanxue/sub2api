//go:build unit

package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// realishOpenAI403Counter 复刻 Redis INCR 的原子语义。
type realishOpenAI403Counter struct {
	mu sync.Mutex
	n  int64
}

func (c *realishOpenAI403Counter) IncrementOpenAI403Count(_ context.Context, _ int64, _ int) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n, nil
}

func (c *realishOpenAI403Counter) ResetOpenAI403Count(_ context.Context, _ int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n = 0
	return nil
}

func (c *realishOpenAI403Counter) value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// 同一故障事件下的并发 403：账号 concurrency 默认 3，一层坏代理或一次权限抖动
// 会让多个 in-flight 请求同时拿到 403。没有升级槽位时计数在毫秒内冲到
// openAI403DisableThreshold，单档 10 分钟冷却被整体跳过、账号直接 SetError
// ——正是 handle403 上方注释要防的「一个坏请求/一层坏代理连环永久禁用整组账号」。
func TestOpenAI403_并发同一事件只推进一轮(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &realishOpenAI403Counter{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(counter)
	svc.SetEscalationSlotCache(newStubEscalationSlots())

	account := &Account{ID: 701, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"message":"Access forbidden"}}`)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
		}()
	}
	wg.Wait()

	require.Equal(t, 0, repo.setErrorCalls, "单次故障事件不得把账号永久禁用")
	require.Equal(t, 1, repo.tempCalls, "同一事件只应落一次冷却")
	require.Equal(t, int64(1), counter.value(), "同一事件只应推进一轮阶梯")
}

func TestOpenAI403_槽位收缩到冷却长度并在恢复时释放(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &realishOpenAI403Counter{}
	slots := newStubEscalationSlots()
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(counter)
	svc.SetEscalationSlotCache(slots)

	account := &Account{ID: 702, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc.HandleUpstreamError(context.Background(), account,
		http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"Access forbidden"}}`))

	require.Equal(t,
		[]int{int((time.Duration(openAI403CooldownMinutesDefault) * time.Minute).Seconds())},
		slots.shrunkTo(EscalationSlotPrefixOpenAI403, 702),
		"槽位必须收缩到本轮冷却长度，使账号重新可调度时槽位恰好过期")

	svc.ResetOpenAI403Counter(context.Background(), 702)
	require.Equal(t, 1, slots.released(EscalationSlotPrefixOpenAI403, 702),
		"账号恢复后残留槽位会把下一次真实故障事件误判成同一轮")
}

// --- Antigravity INTERNAL 500 ---

type realishInternal500Cache struct {
	mu sync.Mutex
	n  int64
}

func (c *realishInternal500Cache) IncrementInternal500Count(_ context.Context, _ int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n, nil
}

func (c *realishInternal500Cache) ResetInternal500Count(_ context.Context, _ int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n = 0
	return nil
}

func (c *realishInternal500Cache) value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

type concurrentInternal500Repo struct {
	AccountRepository
	mu     sync.Mutex
	temps  int
	errors int
}

func (r *concurrentInternal500Repo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.temps++
	return nil
}

func (r *concurrentInternal500Repo) SetError(_ context.Context, _ int64, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors++
	return nil
}

// Google 侧区域性故障时，多个并发请求各自跑完 3 轮重试且全部命中 INTERNAL 500，
// 各自递增计数。没有槽位时计数冲到 internal500PenaltyTier3Threshold，
// 30min / 2h 两档冷却被整体跳过、账号被直接永久禁用。
func TestInternal500_并发同一事件只推进一轮(t *testing.T) {
	repo := &concurrentInternal500Repo{}
	cache := &realishInternal500Cache{}
	svc := &AntigravityGatewayService{
		accountRepo:      repo,
		internal500Cache: cache,
		escalationSlots:  newStubEscalationSlots(),
	}
	account := &Account{ID: 801, Name: "acc-801"}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.handleInternal500RetryExhausted(context.Background(), "[test]", account)
		}()
	}
	wg.Wait()

	require.Equal(t, 0, repo.errors, "单次故障事件不得把账号永久禁用")
	require.Equal(t, 1, repo.temps, "同一事件只应落一次冷却")
	require.Equal(t, int64(1), cache.value(), "同一事件只应推进一轮阶梯")
}

func TestInternal500_槽位收缩到本轮冷却并在成功时释放(t *testing.T) {
	repo := &concurrentInternal500Repo{}
	cache := &realishInternal500Cache{}
	slots := newStubEscalationSlots()
	svc := &AntigravityGatewayService{
		accountRepo:      repo,
		internal500Cache: cache,
		escalationSlots:  slots,
	}
	account := &Account{ID: 802, Name: "acc-802"}

	svc.handleInternal500RetryExhausted(context.Background(), "[test]", account)
	require.Equal(t, []int{int(internal500PenaltyTier1Duration.Seconds())},
		slots.shrunkTo(EscalationSlotPrefixAntigravityInternal500, 802),
		"第 1 轮落 30 分钟冷却，槽位必须收缩到同一长度")

	svc.resetInternal500Counter(context.Background(), "[test]", 802)
	require.Equal(t, 1, slots.released(EscalationSlotPrefixAntigravityInternal500, 802),
		"成功响应后必须释放槽位，否则下一次真实故障被误判成同一轮")
}

func TestInternal500CooldownForCount_对齐实际落下的冷却档(t *testing.T) {
	require.Equal(t, internal500PenaltyTier1Duration, internal500CooldownForCount(1))
	require.Equal(t, internal500PenaltyTier2Duration, internal500CooldownForCount(2))
	// 永久禁用档没有冷却终点，沿用最长一档；此时账号已 SetError，槽位长短不影响调度。
	require.Equal(t, internal500PenaltyTier2Duration,
		internal500CooldownForCount(int64(internal500PenaltyTier3Threshold)))
}

// 管理员清理路径（ClearRateLimit / 清除错误状态）必须把四条阶梯全部清干净。
// 修复前只清 OpenAI 403 / Anthropic 上游错误 / Antigravity validation 三条，漏掉
// INTERNAL 500：计数器靠 24h 兜底 TTL、槽位靠 2h 占位 TTL 自然过期，于是管理员
// 刚清完的账号在接下来最长 2h 内碰到第一次真实故障时，会被残留槽位误判成同一轮
// 而少落一档冷却。
func TestClearRateLimit_四条阶梯的计数与槽位全部清干净(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	slots := newStubEscalationSlots()
	openAI403 := &realishOpenAI403Counter{}
	internal500 := &realishInternal500Cache{}

	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(openAI403)
	svc.SetInternal500CounterCache(internal500)
	svc.SetEscalationSlotCache(slots)

	const accountID int64 = 903
	ctx := context.Background()

	// 先把两条阶梯都推进一轮，制造出待清理的计数与槽位。
	svc.HandleUpstreamError(ctx, &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"Access forbidden"}}`))
	require.Equal(t, int64(1), openAI403.value(), "前置条件：OpenAI 403 阶梯已推进一轮")

	agSvc := &AntigravityGatewayService{
		accountRepo:      &concurrentInternal500Repo{},
		internal500Cache: internal500,
		escalationSlots:  slots,
	}
	agSvc.handleInternal500RetryExhausted(ctx, "[test]", &Account{ID: accountID, Name: "acc-903"})
	require.Equal(t, int64(1), internal500.value(), "前置条件：INTERNAL 500 阶梯已推进一轮")

	require.NoError(t, svc.ClearRateLimit(ctx, accountID))

	require.Equal(t, int64(0), openAI403.value(), "管理员清理必须清零 OpenAI 403 计数")
	require.Equal(t, int64(0), internal500.value(), "管理员清理必须清零 INTERNAL 500 计数")
	require.Equal(t, 1, slots.released(EscalationSlotPrefixOpenAI403, accountID),
		"管理员清理必须释放 OpenAI 403 升级槽位")
	require.Equal(t, 1, slots.released(EscalationSlotPrefixAntigravityInternal500, accountID),
		"管理员清理必须释放 INTERNAL 500 升级槽位，否则 2h 内的第一次真实故障会被误判成同一轮")
	require.Equal(t, 1, slots.released(EscalationSlotPrefixAntigravityValidation, accountID),
		"管理员清理必须释放 Antigravity validation 升级槽位")
}

// nil receiver 上的清理必须静默返回而非 panic：本文件按「service 可能被 nil
// 持有」的假设写了十余处同形状守卫，槽位释放读 s.escalationSlots，守卫顺序
// 一旦写错就会在这里崩。
func TestResetCounters_nil接收者不panic(t *testing.T) {
	var svc *RateLimitService
	require.NotPanics(t, func() {
		svc.ResetOpenAI403Counter(context.Background(), 1)
		svc.ResetInternal500Counter(context.Background(), 1)
		svc.ResetAntigravityValidationCounter(context.Background(), 1)
	})
}

// 计数器缓存未接线时仍必须释放槽位：两者是独立依赖，早年的写法把槽位释放放在
// 计数器 nil 检查之后，于是未接线的部署会静默漏掉释放。
func TestResetCounters_计数器未接线时仍释放槽位(t *testing.T) {
	slots := newStubEscalationSlots()
	svc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	svc.SetEscalationSlotCache(slots)

	svc.ResetOpenAI403Counter(context.Background(), 904)
	svc.ResetInternal500Counter(context.Background(), 904)

	require.Equal(t, 1, slots.released(EscalationSlotPrefixOpenAI403, 904))
	require.Equal(t, 1, slots.released(EscalationSlotPrefixAntigravityInternal500, 904))
}

// 三条阶梯必须使用互不相同的 key 前缀，否则一条阶梯的故障会压住另一条的升级。
func TestEscalationSlotPrefixes_互不重叠(t *testing.T) {
	prefixes := []string{
		EscalationSlotPrefixAntigravityValidation,
		EscalationSlotPrefixOpenAI403,
		EscalationSlotPrefixAntigravityInternal500,
	}
	seen := map[string]bool{}
	for _, p := range prefixes {
		require.NotEmpty(t, p)
		require.False(t, seen[p], "槽位 key 前缀重复：%s", p)
		seen[p] = true
	}
}
