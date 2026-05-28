//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

const capturedDesktop3pUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/1.8555.2 Chrome/146.0.7680.216 Electron/41.6.1 Safari/537.36"

func TestIsClaudeDesktopGatewayUserAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{"captured desktop 3p", capturedDesktop3pUA, true},
		{"claude code cli excluded", "claude-cli/1.2.3", false},
		{"curl", "curl/8.0", false},
		{"empty", "", false},
		{"browser without electron", "Mozilla/5.0 Claude/1.0.0 Safari/537.36", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsClaudeDesktopGatewayUserAgent(tt.ua))
		})
	}
}

func TestIsClaudeDesktopGatewayClient_Context(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	require.False(t, IsClaudeDesktopGatewayClient(ctx))

	ctx = SetClaudeDesktopGatewayClient(ctx, true)
	require.True(t, IsClaudeDesktopGatewayClient(ctx))

	ctx = SetClaudeDesktopGatewayClient(ctx, false)
	require.False(t, IsClaudeDesktopGatewayClient(ctx))
}

func TestResolveGatewayGroup_AllowsDesktopGatewayOnClaudeCodeOnly(t *testing.T) {
	t.Parallel()

	groupID := int64(70)
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:             groupID,
				Platform:       PlatformAnthropic,
				Status:         StatusActive,
				Hydrated:       true,
				ClaudeCodeOnly: true,
			},
		},
	}

	svc := &GatewayService{groupRepo: groupRepo}
	ctx := SetClaudeDesktopGatewayClient(context.Background(), true)

	gotGroup, gotID, err := svc.resolveGatewayGroup(ctx, &groupID)
	require.NoError(t, err)
	require.NotNil(t, gotGroup)
	require.Equal(t, groupID, gotGroup.ID)
	require.NotNil(t, gotID)
	require.Equal(t, groupID, *gotID)
}

func TestResolveGatewayGroup_StillRejectsUnknownClientOnClaudeCodeOnly(t *testing.T) {
	t.Parallel()

	groupID := int64(71)
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:             groupID,
				Platform:       PlatformAnthropic,
				Status:         StatusActive,
				Hydrated:       true,
				ClaudeCodeOnly: true,
			},
		},
	}

	svc := &GatewayService{groupRepo: groupRepo}
	gotGroup, gotID, err := svc.resolveGatewayGroup(context.Background(), &groupID)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	require.Nil(t, gotGroup)
	require.Nil(t, gotID)
}
