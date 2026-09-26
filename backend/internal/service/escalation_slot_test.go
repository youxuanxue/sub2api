//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubEscalationSlots 复刻 Redis SETNX + EXPIRE + DEL 的原子语义，供所有阶梯的
// 并发用例共用。这是共享 owner 的好处之一：从前每条阶梯各写一份槽位 stub。
type stubEscalationSlots struct {
	mu sync.Mutex

	held       map[string]bool
	acquireErr error
	shrinkErr  error
	releaseErr error

	acquireCalls []string
	shrinkTTLs   map[string][]int
	releaseCalls []string
}

func newStubEscalationSlots() *stubEscalationSlots {
	return &stubEscalationSlots{held: map[string]bool{}, shrinkTTLs: map[string][]int{}}
}

func (s *stubEscalationSlots) key(prefix string, accountID int64) string {
	return fmt.Sprintf("%s%d", prefix, accountID)
}

func (s *stubEscalationSlots) AcquireEscalationSlot(
	_ context.Context, keyPrefix string, accountID int64, _ int,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(keyPrefix, accountID)
	s.acquireCalls = append(s.acquireCalls, k)
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	if s.held[k] {
		return false, nil
	}
	s.held[k] = true
	return true, nil
}

func (s *stubEscalationSlots) ShrinkEscalationSlotTTL(
	_ context.Context, keyPrefix string, accountID int64, ttlSeconds int,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shrinkErr != nil {
		return s.shrinkErr
	}
	k := s.key(keyPrefix, accountID)
	s.shrinkTTLs[k] = append(s.shrinkTTLs[k], ttlSeconds)
	return nil
}

func (s *stubEscalationSlots) ReleaseEscalationSlot(
	_ context.Context, keyPrefix string, accountID int64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(keyPrefix, accountID)
	s.releaseCalls = append(s.releaseCalls, k)
	delete(s.held, k)
	return s.releaseErr
}

func (s *stubEscalationSlots) shrunkTo(keyPrefix string, accountID int64) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shrinkTTLs[s.key(keyPrefix, accountID)]
}

func (s *stubEscalationSlots) released(keyPrefix string, accountID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	want := s.key(keyPrefix, accountID)
	for _, got := range s.releaseCalls {
		if got == want {
			n++
		}
	}
	return n
}

func TestBeginEscalationEpisode_首个调用抢到槽位其余被抑制(t *testing.T) {
	slots := newStubEscalationSlots()

	first, proceed := BeginEscalationEpisode(
		context.Background(), slots, "test_slot:account:", 42, 30*time.Minute)
	require.True(t, proceed, "首个调用必须拿到升级许可")

	_, proceed2 := BeginEscalationEpisode(
		context.Background(), slots, "test_slot:account:", 42, 30*time.Minute)
	require.False(t, proceed2, "同一事件的后续调用必须被抑制")

	// 只有赢家的 CommitCooldown 生效。
	first.CommitCooldown(context.Background(), 2*time.Hour)
	require.Equal(t, []int{int((2 * time.Hour).Seconds())}, slots.shrunkTo("test_slot:account:", 42))
}

func TestBeginEscalationEpisode_不同账号互不影响(t *testing.T) {
	slots := newStubEscalationSlots()

	_, a := BeginEscalationEpisode(context.Background(), slots, "p:", 1, time.Minute)
	_, b := BeginEscalationEpisode(context.Background(), slots, "p:", 2, time.Minute)

	require.True(t, a)
	require.True(t, b, "槽位必须按账号隔离，不能让一个账号的故障压住另一个")
}

func TestBeginEscalationEpisode_同账号不同阶梯互不影响(t *testing.T) {
	slots := newStubEscalationSlots()

	_, a := BeginEscalationEpisode(context.Background(), slots, "ladder_a:", 7, time.Minute)
	_, b := BeginEscalationEpisode(context.Background(), slots, "ladder_b:", 7, time.Minute)

	require.True(t, a)
	require.True(t, b, "不同阶梯必须用各自的 key 前缀，不能相互抑制升级")
}

// fail open：守卫不可用时一律放行升级，绝不让 Redis 故障反过来放过真正持续
// 失败的账号。
func TestBeginEscalationEpisode_守卫不可用时failOpen(t *testing.T) {
	t.Run("cache 未接线", func(t *testing.T) {
		ep, proceed := BeginEscalationEpisode(context.Background(), nil, "p:", 1, time.Minute)
		require.True(t, proceed)
		// 零值 episode 的 CommitCooldown 必须是 no-op，不得 panic。
		ep.CommitCooldown(context.Background(), time.Hour)
	})

	t.Run("Redis 报错", func(t *testing.T) {
		slots := newStubEscalationSlots()
		slots.acquireErr = errors.New("redis down")

		ep, proceed := BeginEscalationEpisode(context.Background(), slots, "p:", 1, time.Minute)
		require.True(t, proceed, "槽位故障不得吞掉升级")

		ep.CommitCooldown(context.Background(), time.Hour)
		require.Empty(t, slots.shrunkTo("p:", 1), "没抢到槽位就不该收缩 TTL")
	})

	t.Run("accountID 非法", func(t *testing.T) {
		slots := newStubEscalationSlots()
		_, proceed := BeginEscalationEpisode(context.Background(), slots, "p:", 0, time.Minute)
		require.True(t, proceed)
		require.Empty(t, slots.acquireCalls)
	})
}

// 收缩失败只损失一次升级精度（槽位按占位 TTL 过期，偏保守），不得影响调用方。
func TestEscalationEpisode_收缩失败不影响调用方(t *testing.T) {
	slots := newStubEscalationSlots()
	slots.shrinkErr = errors.New("redis down")

	ep, proceed := BeginEscalationEpisode(context.Background(), slots, "p:", 5, time.Minute)
	require.True(t, proceed)
	ep.CommitCooldown(context.Background(), time.Hour) // 不得 panic
}

func TestReleaseEscalationSlot_释放后可再次抢到(t *testing.T) {
	slots := newStubEscalationSlots()

	_, proceed := BeginEscalationEpisode(context.Background(), slots, "p:", 9, time.Hour)
	require.True(t, proceed)

	_, blocked := BeginEscalationEpisode(context.Background(), slots, "p:", 9, time.Hour)
	require.False(t, blocked)

	ReleaseEscalationSlot(context.Background(), slots, "p:", 9)
	require.Equal(t, 1, slots.released("p:", 9))

	_, again := BeginEscalationEpisode(context.Background(), slots, "p:", 9, time.Hour)
	require.True(t, again, "账号恢复后必须能为下一次真实故障事件重新升级")
}

func TestReleaseEscalationSlot_未接线或非法ID不panic(t *testing.T) {
	ReleaseEscalationSlot(context.Background(), nil, "p:", 1)
	ReleaseEscalationSlot(context.Background(), newStubEscalationSlots(), "p:", 0)
}
