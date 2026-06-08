//go:build integration

package repository

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// OAuth401AfterRefreshCounterSuite 用真实 Redis 验证版本闸门 Lua 脚本的两维度计数语义：
// version-bump（换新 token 仍 401）与 same-version（同一有效 token 跨冷却周期持续 401）。
type OAuth401AfterRefreshCounterSuite struct {
	IntegrationRedisSuite
	cache service.OAuth401AfterRefreshCounterCache
}

func TestOAuth401AfterRefreshCounterSuite(t *testing.T) {
	suite.Run(t, new(OAuth401AfterRefreshCounterSuite))
}

func (s *OAuth401AfterRefreshCounterSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewOAuth401AfterRefreshCounterCache(s.rdb)
}

func (s *OAuth401AfterRefreshCounterSuite) key(accountID int64) string {
	return fmt.Sprintf("%s%d", oauth401AfterRefreshCounterPrefix, accountID)
}

// forceSameElapsed 把 same_at 置 0，模拟「距上次同版本计数已远超 debounce」，
// 使下一次同版本调用确定性地 +1（无需 sleep 等真实时钟）。
func (s *OAuth401AfterRefreshCounterSuite) forceSameElapsed(accountID int64) {
	require.NoError(s.T(), s.rdb.HSet(s.ctx, s.key(accountID), "same_at", 0).Err())
}

func (s *OAuth401AfterRefreshCounterSuite) hgetInt(accountID int64, field string) int64 {
	v, err := s.rdb.HGet(s.ctx, s.key(accountID), field).Int64()
	require.NoError(s.T(), err, "HGET %s", field)
	return v
}

// 首次（无 baseline）→ 种 baseline，返回 (0,0)，hash 落 ver/count=0/same=0/same_at>0。
func (s *OAuth401AfterRefreshCounterSuite) TestSeedReturnsZeroZero() {
	const id = int64(10)
	count, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, 300)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), count)
	require.Equal(s.T(), int64(0), same)
	require.Equal(s.T(), int64(1000), s.hgetInt(id, "ver"))
	require.Equal(s.T(), int64(0), s.hgetInt(id, "count"))
	require.Equal(s.T(), int64(0), s.hgetInt(id, "same"))
	require.Positive(s.T(), s.hgetInt(id, "same_at"), "same_at must be stamped to server time")
}

// 版本递增（其间换过新 token 仍 401）→ count+1，且 same 维度重置到新版本 baseline。
func (s *OAuth401AfterRefreshCounterSuite) TestVersionBumpIncrementsAndResetsSame() {
	const id = int64(11)
	_, _, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, 100)
	require.NoError(s.T(), err)
	// 先攒一个 same 计数。
	s.forceSameElapsed(id)
	_, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), same)

	// 版本递增 → count=1，same 归零。
	count, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 2000, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), count)
	require.Equal(s.T(), int64(0), same)
	require.Equal(s.T(), int64(2000), s.hgetInt(id, "ver"))
	require.Equal(s.T(), int64(0), s.hgetInt(id, "same"))
}

// 同版本：debounce 窗口内的并发突发折叠为 0 增量；跨 debounce 才 +1。
func (s *OAuth401AfterRefreshCounterSuite) TestSameVersionDebounceGatesIncrements() {
	const id = int64(12)
	const debounce = 100

	_, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, debounce) // seed
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), same)

	// 紧接着同版本（同 debounce 窗口内）→ 不增量（折叠突发）。
	_, same, err = s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, debounce)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), same, "burst within debounce must not increment")

	// 模拟跨过一个冷却周期 → +1。
	s.forceSameElapsed(id)
	_, same, err = s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, debounce)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), same)

	// 再紧接着同窗口 → 仍不增量。
	_, same, err = s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, debounce)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), same, "burst after increment must not double-count")

	// 再跨一个周期 → +1 = 2。
	s.forceSameElapsed(id)
	_, same, err = s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, debounce)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(2), same)
}

// 版本变小（请求快照过旧）→ 两维度都不动。
func (s *OAuth401AfterRefreshCounterSuite) TestStaleVersionIsNoOp() {
	const id = int64(13)
	_, _, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 2000, 60, 100)
	require.NoError(s.T(), err)
	s.forceSameElapsed(id)
	_, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 2000, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), same)

	// 过旧版本 → 返回当前 (count=0, same=1)，hash 不变。
	count, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 500, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), count)
	require.Equal(s.T(), int64(1), same)
	require.Equal(s.T(), int64(2000), s.hgetInt(id, "ver"))
}

// Reset 清整 key；其后调用重新种 baseline。
func (s *OAuth401AfterRefreshCounterSuite) TestResetClearsAll() {
	const id = int64(14)
	s.forceSameElapsedSeed(id)

	require.NoError(s.T(), s.cache.ResetOAuth401AfterRefresh(s.ctx, id))
	exists, err := s.rdb.Exists(s.ctx, s.key(id)).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), exists, "reset must DEL the whole key")

	count, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(0), count)
	require.Equal(s.T(), int64(0), same)
}

// forceSameElapsedSeed seeds and bumps same to 1 (helper for ResetClearsAll).
func (s *OAuth401AfterRefreshCounterSuite) forceSameElapsedSeed(id int64) {
	_, _, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, 100)
	require.NoError(s.T(), err)
	s.forceSameElapsed(id)
	_, same, err := s.cache.RecordOAuth401AfterRefresh(s.ctx, id, 1000, 60, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), same)
}
