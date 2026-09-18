//go:build unit

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSaturationRollingKeysCoexistWithLegacyCounters(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	ctx := context.Background()
	rdb := cache.rdb
	openai := &openaiSaturationCounterCache{rdb: rdb}
	ag := &antigravitySaturationCounterCache{rdb: rdb}
	scope := service.AntigravitySaturationScope{AccountID: 7, ModelKey: "test-model"}
	for _, tc := range []struct {
		key       string
		increment func() (int64, error)
		read      func() (int64, error)
	}{
		{anthropicSaturationKey(7), func() (int64, error) { return cache.IncrementSaturation(ctx, 7, 600) }, func() (int64, error) { m, e := cache.GetSaturationBatch(ctx, []int64{7}, 600); return m[7], e }},
		{openaiSaturationKey(7), func() (int64, error) { return openai.IncrementSaturation(ctx, 7, 600) }, func() (int64, error) { m, e := openai.GetSaturationBatch(ctx, []int64{7}, 600); return m[7], e }},
		{antigravitySaturationKey(7, "test-model"), func() (int64, error) { return ag.IncrementSaturation(ctx, 7, "test-model", 600) }, func() (int64, error) {
			m, e := ag.GetSaturationBatch(ctx, []service.AntigravitySaturationScope{scope}, 600)
			return m[scope], e
		}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			// Explicit legacy format: deployment compatibility boundary, not new owner.
			legacy := strings.TrimSuffix(tc.key, ":rolling-v2")
			require.NoError(t, rdb.Set(ctx, legacy, 3, 90*time.Second).Err())
			count, err := tc.increment()
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			count, err = tc.read()
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			// Old code must still be able to increment/read after a blue-green rollback.
			require.NoError(t, rdb.Incr(ctx, legacy).Err())
			values, err := rdb.MGet(ctx, legacy).Result()
			require.NoError(t, err)
			require.Equal(t, []any{"4"}, values)
			count, err = tc.read()
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
		})
	}
}
