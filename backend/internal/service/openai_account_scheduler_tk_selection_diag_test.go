//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollectOpenAICompatSelectionFailureStatsForRequest_RuntimeBlocked(t *testing.T) {
	ctx := context.Background()
	gateway := &OpenAIGatewayService{}
	groupID := int64(2)
	account := &Account{
		ID:          73,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"},
		},
	}
	gateway.BlockAccountScheduling(account, time.Now().Add(time.Minute), "429")

	stats := gateway.collectOpenAICompatSelectionFailureStatsForRequest(
		ctx,
		&groupID,
		PlatformOpenAI,
		"gpt-5.4",
		false,
		OpenAIEndpointCapabilityChatCompletions,
		[]Account{*account},
		nil,
	)

	require.Equal(t, 1, stats.Total)
	require.Equal(t, 0, stats.Eligible)
	require.Equal(t, 1, stats.RuntimeBlocked)
	require.Equal(t, []int64{73}, stats.SampleRuntimeBlockedIDs)
}

func TestOpenAICompatNoCandidateError_LogsCapacityStats(t *testing.T) {
	ctx := context.Background()
	gateway := &OpenAIGatewayService{}
	groupID := int64(2)
	account := &Account{
		ID:          75,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"},
		},
	}
	gateway.BlockAccountScheduling(account, time.Now().Add(time.Minute), "429")

	err := openAICompatNoCandidateError(
		"gpt-5.4",
		PlatformOpenAI,
		false,
		[]Account{*account},
		nil,
		&openAICompatNoCandidateEval{
			ctx:                ctx,
			svc:                gateway,
			groupID:            &groupID,
			platform:           PlatformOpenAI,
			requiredCapability: OpenAIEndpointCapabilityChatCompletions,
		},
	)

	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUnsupportedModel))
}
