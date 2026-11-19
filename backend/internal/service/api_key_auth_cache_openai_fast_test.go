package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotGroupForceOpenAIFastRoundtrip(t *testing.T) {
	groupID := int64(50)
	apiKey := &APIKey{
		ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-fast-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "fast-roundtrip", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, ForceOpenAIFast: true, FreeOpenAIFast: true,
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.Hydrated)
	require.True(t, materialized.Group.ForceOpenAIFast)
	require.True(t, materialized.Group.FreeOpenAIFast)
	require.Equal(t, apiKeyAuthSnapshotVersion, cached.Snapshot.Version)
}

func TestAPIKeyAuthSnapshotGroupForceOpenAIUltrafastRoundtrip(t *testing.T) {
	groupID := int64(51)
	apiKey := &APIKey{
		ID: 83, UserID: 40, GroupID: &groupID, Key: "sk-ultrafast-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "ultrafast-roundtrip", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, ForceOpenAIUltrafast: true,
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"force_openai_ultrafast":true`)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.ForceOpenAIUltrafast)
	require.False(t, materialized.Group.ForceOpenAIFast)
}

func TestAPIKeyAuthSnapshotPreservesQACaptureAndUltrafast(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		svc := &APIKeyService{}
		key := &APIKey{ID: 83, UserID: 40, Key: "sk-qa-ultrafast", Status: StatusActive,
			User:  &User{ID: 40, Status: StatusActive, QACaptureEnabled: enabled},
			Group: &Group{ID: 51, Hydrated: true, ForceOpenAIUltrafast: true}}
		entry := &APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), key)}
		payload, err := json.Marshal(entry)
		require.NoError(t, err)
		var cached APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(payload, &cached))
		got, used, err := svc.applyAuthCacheEntry(key.Key, &cached)
		require.NoError(t, err)
		require.True(t, used)
		require.Equal(t, enabled, got.User.QACaptureEnabled)
		require.True(t, got.Group.ForceOpenAIUltrafast)
		cached.Snapshot.Version = 28
		_, used, err = svc.applyAuthCacheEntry(key.Key, &cached)
		require.NoError(t, err)
		require.False(t, used, "both earlier v28 cache layouts must be rebuilt")
	}
}
