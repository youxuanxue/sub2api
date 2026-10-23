package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentModerationOffDoesNotWalkBody(t *testing.T) {
	cfg := defaultContentModerationConfig()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "false",
			SettingKeyContentModerationConfig: string(raw),
		}},
		&contentModerationTestRepo{},
		&contentModerationTestHashCache{},
		nil, nil, nil, nil, nil,
	)
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("clean ", 4096) + `"}]}`)
	require.False(t, svc.MayInspectBody(context.Background()))
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Model:    "kimi-k3",
		Body:     body,
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Zero(t, svc.bodyInspections.Load(), "a disabled coordinator must not extract the body")
}

func TestContentModerationKeywordStillBlocks(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled = true
	cfg.Mode = ContentModerationModePreBlock
	cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordAndAPI
	cfg.BlockedKeywords = []string{"forbidden"}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(raw),
		}},
		&contentModerationTestRepo{},
		&contentModerationTestHashCache{},
		nil, nil, nil, nil, nil,
	)
	body := []byte(`{"messages":[{"role":"user","content":"this is forbidden text"}]}`)
	require.True(t, svc.MayInspectBody(context.Background()))
	decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
		Protocol: ContentModerationProtocolOpenAIChat,
		Model:    "kimi-k3",
		Body:     body,
	})
	require.NoError(t, err)
	require.True(t, decision.Blocked)
	require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)
	require.Greater(t, svc.bodyInspections.Load(), int64(0))
}
