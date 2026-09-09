//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCandidateFailureCounterFixedWindowAndModelIsolation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	counter := NewOpenAISaturationCounterCache(rdb).(service.CandidateFailureCounter)
	ctx := context.Background()
	a := service.CandidateFailureScope{AccountID: 7, Model: "model-a"}
	b := service.CandidateFailureScope{AccountID: 7, Model: "model-b"}
	c := service.CandidateFailureScope{AccountID: 8, Model: "model-a"}
	_, err := counter.IncrementCandidateFailure(ctx, a, 90)
	require.NoError(t, err)
	mr.FastForward(60 * time.Second)
	for range 2 {
		_, err = counter.IncrementCandidateFailure(ctx, a, 90)
		require.NoError(t, err)
	}
	counts, err := counter.GetCandidateFailures(ctx, []service.CandidateFailureScope{a, b, c})
	require.NoError(t, err)
	require.Equal(t, int64(3), counts[a])
	require.Zero(t, counts[b])
	require.Zero(t, counts[c])
	mr.FastForward(31 * time.Second)
	counts, err = counter.GetCandidateFailures(ctx, []service.CandidateFailureScope{a})
	require.NoError(t, err)
	require.Empty(t, counts, "later failures must not extend the fixed window")
}
