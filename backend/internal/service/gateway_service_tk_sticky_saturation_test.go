//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Reuses fakeSaturationCache, satSettingRepoStub and resetSatCache from
// gateway_service_tk_saturation_penalty_test.go (same package + build tag).

func anthropicStub(id int64) *Account {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
}

func TestShouldClearStickyForSaturation_ThresholdBoundary(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		1: anthropicSaturationThreshold - 1, // just below => keep binding
		2: anthropicSaturationThreshold,     // at threshold => clear (sustained)
		3: anthropicSaturationThreshold + 9, // well above => clear
		4: 0,                                // no hits => keep
	}})

	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(1), "sess"),
		"transient blip below threshold must NOT clear the binding (preserves prompt-cache affinity)")
	require.True(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(2), "sess"),
		"sustained saturation at threshold must clear")
	require.True(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(3), "sess"))
	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(4), "sess"))
}

func TestShouldClearStickyForSaturation_NonAnthropicIgnored(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{9: 100}})
	openai := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), openai, "sess"),
		"saturation counter is anthropic-only; other platforms must never be cleared by it")
}

func TestShouldClearStickyForSaturation_NilCounterIsNoop(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{} // no counter wired => feature inert
	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(1), "sess"))
}

func TestShouldClearStickyForSaturation_NilAccount(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{1: 100}})
	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), nil, "sess"))
}

func TestShouldClearStickyForSaturation_ReadErrorIsBestEffort(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{getErr: context.DeadlineExceeded})
	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(1), "sess"),
		"redis error must not clear a binding (selection must never fail on the preference counter)")
}

func TestShouldClearStickyForSaturation_KillSwitchOff(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{1: 100}})
	gw.settingService = NewSettingService(
		&satSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicSaturatedStubDeprioritizeEnabled: "false",
		}},
		&config.Config{},
	)
	require.False(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(1), "sess"),
		"kill-switch off => sticky binding untouched (shares the de-prioritize feature switch)")
	resetSatCache()
}

func TestShouldClearStickyForSaturation_KillSwitchDefaultOn(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{counts: map[int64]int64{1: anthropicSaturationThreshold}})
	gw.settingService = NewSettingService(&satSettingRepoStub{values: map[string]string{}}, &config.Config{})
	require.True(t, gw.tkShouldClearStickyForSaturation(context.Background(), anthropicStub(1), "sess"),
		"unset switch => default ON => sustained saturation clears")
	resetSatCache()
}
