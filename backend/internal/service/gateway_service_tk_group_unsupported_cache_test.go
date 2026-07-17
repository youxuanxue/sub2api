//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTkGroupUnsupportedModelCacheKey_NormalizesModel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "18\x00gpt-5.4-mini", tkGroupUnsupportedModelCacheKey(18, " GPT-5.4-Mini "))
	require.Equal(t, "", tkGroupUnsupportedModelCacheKey(18, "   "))
	require.Equal(t, "", tkGroupUnsupportedModelCacheKey(0, "gpt-4"))
}

func TestTkGroupUnsupportedModelShortCircuit_Hit(t *testing.T) {
	t.Parallel()
	cache := newTkGroupUnsupportedModelNegativeCache()
	groupID := int64(18)
	cache.put(groupID, "gpt-5.4-mini")

	err := tkGroupUnsupportedModelShortCircuit(cache, &groupID, "gpt5.4-mini")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnsupportedModel)
}

func TestTkGroupUnsupportedModelShortCircuit_Miss(t *testing.T) {
	t.Parallel()
	cache := newTkGroupUnsupportedModelNegativeCache()
	groupID := int64(18)

	err := tkGroupUnsupportedModelShortCircuit(cache, &groupID, "gpt-5.4-mini")
	require.NoError(t, err)
}

func TestTkGroupUnsupportedModelShortCircuit_Expires(t *testing.T) {
	cache := newTkGroupUnsupportedModelNegativeCacheWithTTL(20*time.Millisecond, 10*time.Millisecond)
	groupID := int64(18)
	cache.put(groupID, "gpt-5.4-mini")

	require.Error(t, tkGroupUnsupportedModelShortCircuit(cache, &groupID, "gpt-5.4-mini"))
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, tkGroupUnsupportedModelShortCircuit(cache, &groupID, "gpt-5.4-mini"))
}

func TestTkGroupUnsupportedModelRecordErr_OnlyUnsupported(t *testing.T) {
	t.Parallel()
	cache := newTkGroupUnsupportedModelNegativeCache()
	groupID := int64(18)

	_ = tkGroupUnsupportedModelRecordErr(cache, &groupID, "gpt-5.4-mini", ErrNoAvailableAccounts)
	require.False(t, cache.get(groupID, "gpt-5.4-mini"))

	err := tkGroupUnsupportedModelRecordErr(cache, &groupID, "gpt-5.4-mini",
		errors.Join(ErrUnsupportedModel, errors.New("channel pricing restriction")))
	require.ErrorIs(t, err, ErrUnsupportedModel)
	require.True(t, cache.get(groupID, "gpt-5.4-mini"))
}

func TestSelectAccountWithScheduler_CacheHitShortCircuits(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91002)
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, []*Account{newAPIAccount(91102, 7)})
	cache := newTkGroupUnsupportedModelNegativeCache()
	cache.put(groupID, "gpt-5.4-mini")
	svc.SetTkGroupUnsupportedModelCache(cache)

	_, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.4-mini", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, ErrUnsupportedModel)
	require.Contains(t, err.Error(), "negative-cache")
}

func TestChannelInvalidateCache_FlushesGroupUnsupportedCache(t *testing.T) {
	cache := newTkGroupUnsupportedModelNegativeCache()
	cache.put(18, "gpt-5.4-mini")
	repo := &mockChannelRepository{
		listAllFn: func(_ context.Context) ([]Channel, error) {
			return nil, nil
		},
		getGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return map[int64]string{}, nil
		},
	}
	svc := newTestChannelService(repo)
	svc.SetGroupUnsupportedModelCacheFlusher(cache.flush)
	svc.invalidateCache()
	require.False(t, cache.get(18, "gpt-5.4-mini"))
}

func TestProvideTKGroupUnsupportedModelCache_WiresSharedInstance(t *testing.T) {
	gw := &GatewayService{}
	openai := &OpenAIGatewayService{}
	ch := &ChannelService{}
	_ = ProvideTKGroupUnsupportedModelCache(gw, openai, ch)
	require.Same(t, gw.tkGroupUnsupportedCache, openai.tkGroupUnsupportedCache)
	require.NotNil(t, gw.tkGroupUnsupportedCache)

	gw.tkGroupUnsupportedCache.put(7, "bad-model")
	require.True(t, openai.tkGroupUnsupportedCache.get(7, "bad-model"))
}
