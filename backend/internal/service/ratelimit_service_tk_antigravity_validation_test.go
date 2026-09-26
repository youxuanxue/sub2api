//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// stubAntigravityValidationCounter 让测试直接指定阶梯轮数，避免依赖 Redis。
type stubAntigravityValidationCounter struct {
	mu             sync.Mutex
	count          int64
	incErr         error
	incrementCalls []int64
	resetCalls     []int64
	lastWindow     int

	// 升级槽位：默认每次都抢到（单请求用例的行为与未加槽位时一致）。
	slotAcquireErr  error
	slotTaken       bool
	slotTTLSeconds  []int
	slotResetCalls  []int64
	slotAcquireCall int
}

func (c *stubAntigravityValidationCounter) AcquireAntigravityValidationEscalationSlot(
	_ context.Context, _ int64, ttlSeconds int,
) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slotAcquireCall++
	if c.slotAcquireErr != nil {
		return false, c.slotAcquireErr
	}
	if c.slotTaken {
		return false, nil
	}
	c.slotTaken = true
	_ = ttlSeconds
	return true, nil
}

func (c *stubAntigravityValidationCounter) SetAntigravityValidationEscalationSlotTTL(
	_ context.Context, _ int64, ttlSeconds int,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slotTTLSeconds = append(c.slotTTLSeconds, ttlSeconds)
	return nil
}

func (c *stubAntigravityValidationCounter) ResetAntigravityValidationEscalationSlot(
	_ context.Context, accountID int64,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slotResetCalls = append(c.slotResetCalls, accountID)
	c.slotTaken = false
	return nil
}

func (c *stubAntigravityValidationCounter) IncrementAntigravityValidationCount(
	_ context.Context, accountID int64, windowMinutes int,
) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.incrementCalls = append(c.incrementCalls, accountID)
	c.lastWindow = windowMinutes
	if c.incErr != nil {
		return 0, c.incErr
	}
	return c.count, nil
}

func (c *stubAntigravityValidationCounter) ResetAntigravityValidationCount(
	_ context.Context, accountID int64,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetCalls = append(c.resetCalls, accountID)
	return nil
}

func antigravityValidationBody() []byte {
	return []byte(`{"error":{"status":"PERMISSION_DENIED","message":"VALIDATION_REQUIRED: Verify your account to continue.","details":[{"metadata":{"validation_url":"https://accounts.google.com/verify"}}]}}`)
}

func newAntigravityValidationService(
	repo *rateLimitAccountRepoStub, counter AntigravityValidationCounterCache,
) *RateLimitService {
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAntigravityValidationCounterCache(counter)
	return service
}

func antigravityOAuthAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
}

func TestAntigravityValidation403_第1轮临时停调30分钟(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(801),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable, "当前请求必须 failover")
	require.Equal(t, 0, repo.setErrorCalls, "第 1 轮不得永久禁用可恢复的验证挑战")
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "Validation required (403)")
	require.Contains(t, repo.lastTempReason, "validation_url: https://accounts.google.com/verify")
	require.Contains(t, repo.lastTempReason, "(1/3)")
	require.WithinDuration(t, started.Add(30*time.Minute), repo.lastTempUntil, 5*time.Second)
	require.Equal(t, []int64{801}, counter.incrementCalls)
	require.Equal(t, antigravityValidationCounterWindowMinutes, counter.lastWindow)
}

func TestAntigravityValidation403_第2轮升级到2小时(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 2}
	service := newAntigravityValidationService(repo, counter)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(802),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "第 2 轮仍是临时停调")
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "(2/3)")
	require.WithinDuration(t, started.Add(2*time.Hour), repo.lastTempUntil, 5*time.Second)
}

func TestAntigravityValidation403_第3轮永久禁用(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 3}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(803),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "第 3 轮转人工处理，停止无限重放")
	require.Equal(t, 0, repo.tempCalls, "永久禁用时不再写临时停调")
	require.Contains(t, repo.lastErrorMsg, "consecutive_validation_403=3/3")
	require.Contains(t, repo.lastErrorMsg, "Validation required (403)")
}

func TestAntigravityValidation403_计数器缺失时退回单档冷却(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	// 不接线计数器：修复前的行为必须原样保留，绝不因缺少可选依赖而永久禁用。
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(804),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.WithinDuration(t, started.Add(30*time.Minute), repo.lastTempUntil, 5*time.Second)
	require.NotContains(t, repo.lastTempReason, "/3)", "无计数器时不应声称阶梯轮数")
}

func TestAntigravityValidation403_计数失败时退回单档冷却(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{incErr: errors.New("redis down")}
	service := newAntigravityValidationService(repo, counter)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(805),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "计数失败不得把可恢复挑战升级成永久禁用")
	require.Equal(t, 1, repo.tempCalls)
	require.WithinDuration(t, started.Add(30*time.Minute), repo.lastTempUntil, 5*time.Second)
}

func TestAntigravityValidation403_持久化失败时failClosed(t *testing.T) {
	repo := &rateLimitAccountRepoStub{tempErr: errors.New("db down")}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(806),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, 1, repo.setErrorCalls, "冷却写不进去时必须 fail closed，避免紧循环重试")
}

func TestAntigravityValidation403_违规403仍然永久禁用且不计入验证阶梯(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(807),
		http.StatusForbidden, http.Header{},
		[]byte(`{"error":{"message":"Terms of service violation"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Account violation (403)")
	require.Empty(t, counter.incrementCalls, "违规 403 不得消耗验证阶梯预算")
}

func TestResetAntigravityValidationCounter_清零轮数(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	service := newAntigravityValidationService(repo, counter)

	service.ResetAntigravityValidationCounter(context.Background(), 808)
	require.Equal(t, []int64{808}, counter.resetCalls)

	// 无效 accountID 与未接线计数器都不应 panic 或产生写入。
	service.ResetAntigravityValidationCounter(context.Background(), 0)
	require.Equal(t, []int64{808}, counter.resetCalls)

	bare := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	bare.ResetAntigravityValidationCounter(context.Background(), 809)
}

// 同一次验证事件下的并发 403：账号 concurrency 默认 3，三个 in-flight 请求
// 同时拿到 VALIDATION_REQUIRED。升级槽位必须让计数只推进一轮——否则计数在
// 毫秒内冲到 3，两档冷却被跳过，一次本可自动恢复的验证挑战把账号永久禁用。
func TestAntigravityValidation403_并发同一事件只推进一轮(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &realishAntigravityValidationCounter{}
	service := newAntigravityValidationService(repo, counter)
	account := antigravityOAuthAccount(901)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.HandleUpstreamError(
				context.Background(), account,
				http.StatusForbidden, http.Header{}, antigravityValidationBody(),
			)
		}()
	}
	wg.Wait()

	require.Equal(t, 0, repo.setErrorCalls, "单次验证事件不得把账号永久禁用")
	require.Equal(t, 1, repo.tempCalls, "同一事件只应落一次冷却")
	require.Equal(t, int64(1), counter.value(), "同一事件只应推进一轮阶梯")
}

func TestAntigravityValidation403_槽位输家不推进阶梯(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1, slotTaken: true}
	service := newAntigravityValidationService(repo, counter)

	shouldDisable := service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(902),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.True(t, shouldDisable, "输家仍须 failover")
	require.Empty(t, counter.incrementCalls, "输家不得递增计数")
	require.Equal(t, 0, repo.tempCalls, "输家不得重写冷却")
	require.Equal(t, 0, repo.setErrorCalls)
}

func TestAntigravityValidation403_槽位收缩到本轮冷却长度(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 2}
	service := newAntigravityValidationService(repo, counter)

	service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(903),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	// 第 2 轮落 2 小时冷却，槽位必须收缩到同一长度，使账号重新可调度时槽位恰好过期。
	require.Equal(t, []int{int((2 * time.Hour).Seconds())}, counter.slotTTLSeconds)
}

// 槽位不可用（Redis 故障）时必须 fail open 照常升级，不能让守卫故障放过
// 真正需要人工验证的账号。
func TestAntigravityValidation403_槽位故障时照常升级(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{
		count:          1,
		slotAcquireErr: errors.New("redis down"),
	}
	service := newAntigravityValidationService(repo, counter)

	service.HandleUpstreamError(
		context.Background(), antigravityOAuthAccount(904),
		http.StatusForbidden, http.Header{}, antigravityValidationBody(),
	)

	require.Len(t, counter.incrementCalls, 1, "槽位故障不得吞掉升级")
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, counter.slotTTLSeconds, "没抢到槽位就不该收缩 TTL")
}

func TestResetAntigravityValidationCounter_同时释放槽位(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1, slotTaken: true}
	service := newAntigravityValidationService(repo, counter)

	service.ResetAntigravityValidationCounter(context.Background(), 905)

	require.Equal(t, []int64{905}, counter.resetCalls)
	require.Equal(t, []int64{905}, counter.slotResetCalls,
		"账号恢复后残留槽位会把下一次真实验证事件误判成同一轮")
}

// realishAntigravityValidationCounter 复刻 Redis INCR + SETNX 的原子语义，
// 用于并发用例。
type realishAntigravityValidationCounter struct {
	mu   sync.Mutex
	n    int64
	slot bool
}

func (c *realishAntigravityValidationCounter) value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *realishAntigravityValidationCounter) IncrementAntigravityValidationCount(
	_ context.Context, _ int64, _ int,
) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n, nil
}

func (c *realishAntigravityValidationCounter) ResetAntigravityValidationCount(
	_ context.Context, _ int64,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n = 0
	return nil
}

func (c *realishAntigravityValidationCounter) AcquireAntigravityValidationEscalationSlot(
	_ context.Context, _ int64, _ int,
) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slot {
		return false, nil
	}
	c.slot = true
	return true, nil
}

func (c *realishAntigravityValidationCounter) SetAntigravityValidationEscalationSlotTTL(
	_ context.Context, _ int64, _ int,
) error {
	return nil
}

func (c *realishAntigravityValidationCounter) ResetAntigravityValidationEscalationSlot(
	_ context.Context, _ int64,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slot = false
	return nil
}
