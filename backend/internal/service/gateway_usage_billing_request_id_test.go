//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestResolveUsageBillingRequestID_ForcedWebSearchBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-shared-id")
	got := resolveUsageBillingRequestID(ctx, "web_search:uuid-1")
	require.Equal(t, "web_search:uuid-1", got)
}

func TestResolveUsageBillingRequestID_ClientMarkerCannotOverrideUpstream(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-shared-id")
	got := resolveUsageBillingRequestID(ctx, "resp_abc")
	require.Equal(t, "resp_abc", got)
}

func TestIsForcedUsageBillingRequestID(t *testing.T) {
	t.Parallel()
	require.True(t, isForcedUsageBillingRequestID("web_search:x"))
	require.True(t, isForcedUsageBillingRequestID("grok-video:task-1"))
	require.True(t, isForcedUsageBillingRequestID("grok_audio:up-1"))
	require.True(t, isForcedUsageBillingRequestID("grok_realtime:sess-1"))
	require.False(t, isForcedUsageBillingRequestID("resp_abc"))
}

func TestStableGrokAudioBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_audio:up-1", StableGrokAudioBillingRequestID("up-1"))
	require.Equal(t, "grok_audio:up-1", StableGrokAudioBillingRequestID("grok_audio:up-1"))
	got := StableGrokAudioBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_audio:"))
	require.Greater(t, len(got), len("grok_audio:"))
}

func TestStableGrokRealtimeBillingRequestID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok_realtime:s1", StableGrokRealtimeBillingRequestID("s1"))
	require.Equal(t, "grok_realtime:s1", StableGrokRealtimeBillingRequestID("grok_realtime:s1"))
	got := StableGrokRealtimeBillingRequestID("")
	require.True(t, strings.HasPrefix(got, "grok_realtime:"))
}

func TestResolveUsageBillingRequestID_ForcedGrokAudioBeatsClientID(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-shared-id")
	got := resolveUsageBillingRequestID(ctx, StableGrokAudioBillingRequestID("up-9"))
	require.Equal(t, "grok_audio:up-9", got)
}

func TestUsageBillingIgnoresReusedClientMarker(t *testing.T) {
	var ids []string
	for _, serverID := range []string{"server-one", "server-two"} {
		ctx := context.WithValue(context.Background(), ctxkey.RequestID, serverID)
		ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "reused-client-marker")
		id := resolveUsageBillingRequestID(ctx, "upstream-id")
		require.Equal(t, "local:"+serverID, id)
		require.Equal(t, id, resolveUsageBillingRequestID(ctx, "retry-upstream-id"), "one served request keeps a stable settlement identity")
		require.Equal(t, "local:"+serverID, resolveUsageBillingPayloadFingerprint(ctx, ""))
		ids = append(ids, id)
	}
	require.NotEqual(t, ids[0], ids[1], "two served calls must not share the billing dedup key")
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "reused-client-marker")
	require.Equal(t, "upstream-id", resolveUsageBillingRequestID(ctx, "upstream-id"))
	require.Empty(t, resolveUsageBillingPayloadFingerprint(ctx, ""))
}
