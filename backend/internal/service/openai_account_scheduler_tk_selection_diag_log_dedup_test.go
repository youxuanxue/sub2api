//go:build unit

package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTkOpenAICompatSelectionFailureLogDedup_SuppressesRepeatsWithinTTL(t *testing.T) {
	dedup := newTkOpenAICompatSelectionFailureLogDedupWithTTL(20*time.Millisecond, 10*time.Millisecond)
	stats := selectionFailureStats{Total: 3, RuntimeBlocked: 3}

	require.True(t, dedup.shouldLog(6, PlatformOpenAI, "gpt-5.4", stats))
	require.False(t, dedup.shouldLog(6, PlatformOpenAI, "gpt-5.4", stats))
	require.True(t, dedup.shouldLog(6, PlatformOpenAI, "gpt-5.4-mini", stats))

	time.Sleep(25 * time.Millisecond)
	require.True(t, dedup.shouldLog(6, PlatformOpenAI, "gpt-5.4", stats))
}

func TestTkOpenAICompatSelectionFailureLogDedup_DifferentFailureSignatureLogsAgain(t *testing.T) {
	dedup := newTkOpenAICompatSelectionFailureLogDedup()
	statsBlocked := selectionFailureStats{Total: 2, RuntimeBlocked: 2}
	statsUnsupported := selectionFailureStats{Total: 2, ModelUnsupported: 2}

	require.True(t, dedup.shouldLog(9, PlatformOpenAI, "gpt-5.4", statsBlocked))
	require.True(t, dedup.shouldLog(9, PlatformOpenAI, "gpt-5.4", statsUnsupported))
}

func TestSelectionFailureStatsFingerprint_IgnoresSampleIDs(t *testing.T) {
	a := selectionFailureStats{Total: 4, RuntimeBlocked: 2, SampleRuntimeBlockedIDs: []int64{1, 2}}
	b := selectionFailureStats{Total: 4, RuntimeBlocked: 2, SampleRuntimeBlockedIDs: []int64{99}}
	require.Equal(t, selectionFailureStatsFingerprint(a), selectionFailureStatsFingerprint(b))
}

func TestOpenAICompatSelectionFailureOpsSystemLogRedundant(t *testing.T) {
	require.False(t, OpenAICompatSelectionFailureOpsSystemLogRedundant(nil))
	require.True(t, OpenAICompatSelectionFailureOpsSystemLogRedundant(ErrNoAvailableAccounts))
	require.True(t, OpenAICompatSelectionFailureOpsSystemLogRedundant(ErrNoAvailableCompactAccounts))
	require.True(t, OpenAICompatSelectionFailureOpsSystemLogRedundant(ErrUnsupportedModel))
	require.False(t, OpenAICompatSelectionFailureOpsSystemLogRedundant(fmt.Errorf("query accounts failed")))
}
