//go:build integration

package repository

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// AnthropicBodyless403CounterSuite 用真实 Redis 验证空 body 403 计数器的 debounce 语义：
// 一个失败 episode 内的并发突发折叠成 1 次计数，跨 debounce 才 +1——这是防止瞬时突发把
// 健康账号永久禁用的安全闸（R-001）。
type AnthropicBodyless403CounterSuite struct {
	IntegrationRedisSuite
	cache service.AnthropicUpstreamErrorCounterCache
}

func TestAnthropicBodyless403CounterSuite(t *testing.T) {
	suite.Run(t, new(AnthropicBodyless403CounterSuite))
}

func (s *AnthropicBodyless403CounterSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewAnthropicUpstreamErrorCounterCache(s.rdb)
}

func (s *AnthropicBodyless403CounterSuite) key(accountID int64) string {
	return fmt.Sprintf("%s%d", anthropicBodyless403CounterPrefix, accountID)
}

// forceElapsed 把 at 置 0，模拟「距上次计数已远超 debounce」，使下一次确定性 +1（无需 sleep）。
func (s *AnthropicBodyless403CounterSuite) forceElapsed(accountID int64) {
	require.NoError(s.T(), s.rdb.HSet(s.ctx, s.key(accountID), "at", 0).Err())
}

func (s *AnthropicBodyless403CounterSuite) hgetInt(accountID int64, field string) int64 {
	v, err := s.rdb.HGet(s.ctx, s.key(accountID), field).Int64()
	require.NoError(s.T(), err, "HGET %s", field)
	return v
}

// 首次 → count=1，hash 落 count=1/at>0。
func (s *AnthropicBodyless403CounterSuite) TestSeedReturnsOne() {
	const id = int64(20)
	count, err := s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), count)
	require.Equal(s.T(), int64(1), s.hgetInt(id, "count"))
	require.Positive(s.T(), s.hgetInt(id, "at"), "at must be stamped to server time")
}

// 同 episode 并发突发（debounce 窗口内）→ 不增量，保持 1。
func (s *AnthropicBodyless403CounterSuite) TestBurstWithinDebounceDoesNotIncrement() {
	const id = int64(21)
	const debounce = 100

	count, err := s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, debounce)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), count)

	// 模拟 10 个同时在途的请求各打一次空 body 403：全部落在同一 debounce 窗口内。
	for i := 0; i < 10; i++ {
		count, err = s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, debounce)
		require.NoError(s.T(), err)
		require.Equal(s.T(), int64(1), count, "并发突发必须折叠成 1，否则健康账号会被瞬时抖动永久禁用")
	}
}

// 跨 debounce（不同 episode）→ 每个 episode +1。
func (s *AnthropicBodyless403CounterSuite) TestDistinctEpisodesIncrement() {
	const id = int64(22)
	const debounce = 100

	count, err := s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, debounce)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), count)

	for want := int64(2); want <= 5; want++ {
		s.forceElapsed(id) // 模拟跨过一个冷却周期
		count, err = s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, debounce)
		require.NoError(s.T(), err)
		require.Equal(s.T(), want, count)
		// 同 episode 内紧接着的突发仍不重复计数。
		count, err = s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, debounce)
		require.NoError(s.T(), err)
		require.Equal(s.T(), want, count, "episode 内突发不得重复计数")
	}
}

// Reset 清整 key；其后重新种 baseline。
func (s *AnthropicBodyless403CounterSuite) TestResetClearsAll() {
	const id = int64(23)
	_, err := s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, 100)
	require.NoError(s.T(), err)

	require.NoError(s.T(), s.cache.ResetAnthropicBodyless403Count(s.ctx, id))
	exists, err := s.rdb.Exists(s.ctx, s.key(id)).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), exists, "reset must DEL the whole key")

	count, err := s.cache.IncrementAnthropicBodyless403Count(s.ctx, id, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), count)
}
