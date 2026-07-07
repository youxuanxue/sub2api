//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestShouldClearOpenAIStickyForSaturation_ThresholdBoundary(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		63: openAIEdgeMirrorStubSaturationThreshold - 1,
		64: openAIEdgeMirrorStubSaturationThreshold,
	}})

	require.False(t, svc.tkShouldClearOpenAIStickyForSaturation(context.Background(), openAIEdgeStub(63), "sess"))
	require.True(t, svc.tkShouldClearOpenAIStickyForSaturation(context.Background(), openAIEdgeStub(64), "sess"))
}

func TestShouldClearOpenAIStickyForSaturation_NonEdgeStubIgnored(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{9: 100}})
	oauth := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, svc.tkShouldClearOpenAIStickyForSaturation(context.Background(), oauth, "sess"))
}

func TestShouldClearOpenAIStickyForSaturation_KillSwitchOff(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{63: 100}})
	svc.settingService = NewSettingService(
		&satSettingRepoStub{values: map[string]string{
			SettingKeyOpenAISaturatedStubDeprioritizeEnabled: "false",
		}},
		&config.Config{},
	)
	require.False(t, svc.tkShouldClearOpenAIStickyForSaturation(context.Background(), openAIEdgeStub(63), "sess"))
	resetOpenAISatCache()
}
