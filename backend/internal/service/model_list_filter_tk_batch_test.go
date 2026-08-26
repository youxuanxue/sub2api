package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type batchAvailabilityRepoStub struct {
	states      map[string]AvailabilityState
	getCalls    int
	batchCalls  int
	batchInputs [][]string
}

func (s *batchAvailabilityRepoStub) Upsert(context.Context, string, string, func(AvailabilityState) AvailabilityState) error {
	return nil
}

func (s *batchAvailabilityRepoStub) Get(_ context.Context, platform, modelID string) (AvailabilityState, error) {
	s.getCalls++
	return s.states[platform+"/"+modelID], nil
}

func (s *batchAvailabilityRepoStub) GetBatch(_ context.Context, platform string, modelIDs []string) (map[string]AvailabilityState, error) {
	s.batchCalls++
	s.batchInputs = append(s.batchInputs, append([]string(nil), modelIDs...))
	out := make(map[string]AvailabilityState, len(modelIDs))
	for _, modelID := range modelIDs {
		if state, ok := s.states[platform+"/"+modelID]; ok {
			out[modelID] = state
		}
	}
	return out, nil
}

func TestModelListFilterStrict_BatchesPricedAvailabilityReads(t *testing.T) {
	pricing := NewPricingCatalogService(nil)
	pricing.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(`{
			"model-a":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
			"model-b":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"}
		}`), time.Unix(1, 0), true
	})
	repo := &batchAvailabilityRepoStub{states: map[string]AvailabilityState{
		"openai/model-b": {Platform: "openai", ModelID: "model-b", Status: AvailabilityStatusUnreachable},
	}}
	filter := NewModelListFilter(pricing, NewPricingAvailabilityService(repo, time.Now))

	got, err := filter.FilterClientFacingStrict(
		context.Background(),
		PlatformOpenAI,
		[]string{"model-a", "unpriced-model", "model-b"},
	)

	require.NoError(t, err)
	require.Equal(t, []string{"model-a"}, got)
	require.Zero(t, repo.getCalls, "strict discovery must not issue one availability query per model")
	require.Equal(t, 1, repo.batchCalls)
	require.Equal(t, [][]string{{"model-a", "model-b"}}, repo.batchInputs)
}

func TestModelListFilterStrict_ReusesAvailabilityReadsWithinDiscoveryRequest(t *testing.T) {
	pricing := NewPricingCatalogService(nil)
	pricing.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(`{
			"model-a":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
			"model-b":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
			"model-c":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"}
		}`), time.Unix(1, 0), true
	})
	repo := &batchAvailabilityRepoStub{states: map[string]AvailabilityState{}}
	filter := NewModelListFilter(pricing, NewPricingAvailabilityService(repo, time.Now))
	ctx := withModelAvailabilityRequestCache(context.Background())

	first, err := filter.FilterClientFacingStrict(ctx, PlatformOpenAI, []string{"model-a", "model-b"})
	require.NoError(t, err)
	second, err := filter.FilterClientFacingStrict(ctx, PlatformOpenAI, []string{"model-b", "model-c"})
	require.NoError(t, err)

	require.Equal(t, []string{"model-a", "model-b"}, first)
	require.Equal(t, []string{"model-b", "model-c"}, second)
	require.Zero(t, repo.getCalls)
	require.Equal(t, 2, repo.batchCalls)
	require.Equal(t, [][]string{{"model-a", "model-b"}, {"model-c"}}, repo.batchInputs)
}

func TestUniversalCapabilityList_ReusesBatchedAvailabilityForCatalogFallbackGroups(t *testing.T) {
	groups := []Group{
		us046ActiveGroup(10, PlatformOpenAI, true),
		us046ActiveGroup(20, PlatformOpenAI, true),
	}
	repo := &batchAvailabilityRepoStub{states: map[string]AvailabilityState{}}
	filter := NewModelListFilter(nil, NewPricingAvailabilityService(repo, time.Now))
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{groups: groups},
		func(context.Context, int64, string) ([]string, bool, error) {
			return nil, true, nil
		},
		func(_ context.Context, _ int64, _ string, _ string, shape UniversalShape) (bool, error) {
			return shape == ShapeOpenAIChat, nil
		},
		func(ctx context.Context, platform string) ([]string, error) {
			return filter.ServableClientFacingIDsStrict(ctx, platform)
		},
	)

	got, err := svc.List(
		context.Background(),
		&APIKey{UserID: 9, RoutingMode: RoutingModeUniversal},
		UniversalProtocolCodex,
	)

	require.NoError(t, err)
	require.NotEmpty(t, got)
	require.Zero(t, repo.getCalls, "catalog fallback must not issue one availability read per model")
	require.Equal(t, 1, repo.batchCalls, "same-platform fallback groups must share one request-local batch")
}
