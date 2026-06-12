//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// --- stubs ---

// fakeSaturationCache implements AnthropicSaturationCounterCache from a static
// per-account count map, so score-side tests run without Redis.
type fakeSaturationCache struct {
	counts map[int64]int64
	getErr error
}

func (f *fakeSaturationCache) IncrementSaturation(_ context.Context, accountID int64, _ int) (int64, error) {
	if f.counts == nil {
		f.counts = map[int64]int64{}
	}
	f.counts[accountID]++
	return f.counts[accountID], nil
}

func (f *fakeSaturationCache) GetSaturation(_ context.Context, accountID int64) (int64, error) {
	if f.getErr != nil {
		return 0, f.getErr
	}
	return f.counts[accountID], nil
}

func (f *fakeSaturationCache) GetSaturationBatch(_ context.Context, ids []int64) (map[int64]int64, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := map[int64]int64{}
	for _, id := range ids {
		if n := f.counts[id]; n != 0 {
			out[id] = n
		}
	}
	return out, nil
}

// satSettingRepoStub feeds SettingService.GetValue for the kill-switch tests.
type satSettingRepoStub struct{ values map[string]string }

func (r *satSettingRepoStub) Get(_ context.Context, key string) (*Setting, error) {
	if v, ok := r.values[key]; ok {
		return &Setting{Key: key, Value: v}, nil
	}
	return nil, ErrSettingNotFound
}
func (r *satSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	s, err := r.Get(context.Background(), key)
	if err != nil {
		return "", err
	}
	return s.Value, nil
}
func (r *satSettingRepoStub) Set(_ context.Context, _, _ string) error { return nil }
func (r *satSettingRepoStub) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *satSettingRepoStub) SetMultiple(_ context.Context, _ map[string]string) error { return nil }
func (r *satSettingRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *satSettingRepoStub) Delete(_ context.Context, _ string) error { return nil }

func anthropicAccWithLoad(id int64, priority int, loadRate int, lastUsed *time.Time) accountWithLoad {
	return accountWithLoad{
		account: &Account{
			ID:         id,
			Priority:   priority,
			Platform:   PlatformAnthropic,
			Type:       AccountTypeAPIKey,
			LastUsedAt: lastUsed,
		},
		loadInfo: &AccountLoadInfo{AccountID: id, LoadRate: loadRate},
	}
}

// resetSatCache clears the process-level kill-switch cache between subtests so a
// prior subtest's cached decision does not leak.
func resetSatCache() { satDeprioritizeCache.Store((*satDeprioritizeCacheEntry)(nil)) }

// --- effectivePriority / filterByMinPriority ---

func TestEffectivePriority_FoldsPenalty(t *testing.T) {
	a := anthropicAccWithLoad(1, 5, 0, nil)
	require.Equal(t, 5, a.effectivePriority(), "no penalty => base priority")
	a.saturationPenalty = anthropicSaturationPriorityPenalty
	require.Equal(t, 5+anthropicSaturationPriorityPenalty, a.effectivePriority())
}

func TestFilterByMinPriority_SaturatedSortsAfterFresh(t *testing.T) {
	// Two equal-base-priority stubs; one carries the saturation penalty.
	fresh := anthropicAccWithLoad(1, 0, 50, nil)
	saturated := anthropicAccWithLoad(2, 0, 0, nil) // lower load, but saturated
	saturated.saturationPenalty = anthropicSaturationPriorityPenalty

	result := filterByMinPriority([]accountWithLoad{saturated, fresh})
	require.Len(t, result, 1, "penalty must break the priority tie")
	require.Equal(t, int64(1), result[0].account.ID, "fresh stub wins despite higher load")
}

func TestFilterByMinPriority_AllSaturatedPreservesOrder(t *testing.T) {
	// Every candidate saturated => same additive penalty => base ordering intact,
	// and the set is NON-EMPTY (bounded penalty never excludes — amplifier-safe).
	a := anthropicAccWithLoad(1, 1, 0, nil)
	b := anthropicAccWithLoad(2, 2, 0, nil)
	c := anthropicAccWithLoad(3, 1, 0, nil)
	for _, p := range []*accountWithLoad{&a, &b, &c} {
		p.saturationPenalty = anthropicSaturationPriorityPenalty
	}
	result := filterByMinPriority([]accountWithLoad{a, b, c})
	// Base min priority is 1 (ids 1,3); id 2 (priority 2) sorts after. Both
	// priority-1 entries survive because relative order is preserved.
	require.Len(t, result, 2)
	ids := map[int64]bool{result[0].account.ID: true, result[1].account.ID: true}
	require.True(t, ids[1] && ids[3], "all-saturated still returns the min-base-priority bucket")
}

// --- computeAnthropicSaturationPenalties ---

func TestComputePenalties_BelowThresholdNoPenalty(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		1: anthropicSaturationThreshold - 1, // just below
	}})
	cands := []accountWithLoad{anthropicAccWithLoad(1, 0, 0, nil)}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, 0, cands[0].saturationPenalty, "below threshold => no penalty (current behaviour preserved)")
}

func TestComputePenalties_AtThresholdPenalized(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		1: anthropicSaturationThreshold,     // at threshold => penalized
		2: anthropicSaturationThreshold + 5, // above => penalized
		3: 0,                                // fresh => untouched
	}})
	cands := []accountWithLoad{
		anthropicAccWithLoad(1, 0, 0, nil),
		anthropicAccWithLoad(2, 0, 0, nil),
		anthropicAccWithLoad(3, 0, 0, nil),
	}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, anthropicSaturationPriorityPenalty, cands[0].saturationPenalty)
	require.Equal(t, anthropicSaturationPriorityPenalty, cands[1].saturationPenalty)
	require.Equal(t, 0, cands[2].saturationPenalty)
}

func TestComputePenalties_NonAnthropicIgnored(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{9: 100}})
	cands := []accountWithLoad{{
		account:  &Account{ID: 9, Platform: PlatformOpenAI, Priority: 0},
		loadInfo: &AccountLoadInfo{AccountID: 9},
	}}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, 0, cands[0].saturationPenalty, "non-anthropic platform must never be penalized by this feature")
}

func TestComputePenalties_ReadErrorIsBestEffort(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{getErr: context.DeadlineExceeded})
	cands := []accountWithLoad{anthropicAccWithLoad(1, 0, 0, nil)}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, 0, cands[0].saturationPenalty, "redis error => no penalty (selection must not fail)")
}

func TestComputePenalties_NilCacheIsNoop(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{} // no cache wired => feature disabled
	cands := []accountWithLoad{anthropicAccWithLoad(1, 0, 0, nil)}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, 0, cands[0].saturationPenalty)
}

func TestComputePenalties_KillSwitchOff(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{1: 100}})
	gw.settingService = NewSettingService(
		&satSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicSaturatedStubDeprioritizeEnabled: "false",
		}},
		&config.Config{},
	)
	cands := []accountWithLoad{anthropicAccWithLoad(1, 0, 0, nil)}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, 0, cands[0].saturationPenalty, "kill-switch off => exact pre-feature scoring (no penalty)")
	resetSatCache()
}

func TestComputePenalties_KillSwitchDefaultOn(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{1: 100}})
	// Empty setting => default ON.
	gw.settingService = NewSettingService(&satSettingRepoStub{values: map[string]string{}}, &config.Config{})
	cands := []accountWithLoad{anthropicAccWithLoad(1, 0, 0, nil)}
	gw.computeAnthropicSaturationPenalties(context.Background(), cands)
	require.Equal(t, anthropicSaturationPriorityPenalty, cands[0].saturationPenalty, "default ON => penalty applied")
	resetSatCache()
}

func TestHasAnthropicSaturationCounter(t *testing.T) {
	gw := &GatewayService{}
	require.False(t, gw.HasAnthropicSaturationCounter())
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{})
	require.True(t, gw.HasAnthropicSaturationCounter())
}
