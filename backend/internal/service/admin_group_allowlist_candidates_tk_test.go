//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Group allowlist picker SSOT: only schedulable members' model_mapping keys.
func TestGetGroupModelsListCandidates_MembersMappingUnionOnly(t *testing.T) {
	accountRepo := &accountRepoStubForCompositeModelsList{
		accounts: []Account{
			{
				ID:       1,
				Platform: PlatformNewAPI,
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"kimi-k3":         "kimi-k3",
						"deepseek-v4-pro": "deepseek-v4-pro",
						"glm-5.3":         "glm-5.3",
					},
				},
			},
			{
				// Wrong-platform member must not contribute.
				ID:       2,
				Platform: PlatformAnthropic,
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-sonnet-4-6": "claude-sonnet-4-6",
					},
				},
			},
		},
	}
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			690: {ID: 690, Platform: PlatformNewAPI},
		},
	}
	svc := &adminServiceImpl{accountRepo: accountRepo, groupRepo: groupRepo}

	candidates, err := svc.GetGroupModelsListCandidates(context.Background(), 690, PlatformNewAPI)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek-v4-pro", "glm-5.3", "kimi-k3"}, candidates)
	for _, id := range candidates {
		require.NotContains(t, id, "claude", "Claude must not leak into newapi group allowlist candidates")
	}
}

func TestGetGroupModelsListCandidates_EmptyWithoutMembers(t *testing.T) {
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Platform: PlatformAnthropic},
		},
	}
	svc := &adminServiceImpl{
		accountRepo: &accountRepoStubForCompositeModelsList{accounts: nil},
		groupRepo:   groupRepo,
	}

	candidates, err := svc.GetGroupModelsListCandidates(context.Background(), 1, PlatformAnthropic)
	require.NoError(t, err)
	require.Empty(t, candidates, "no members → empty picker; do not seed Claude defaults")

	candidates, err = svc.GetGroupModelsListCandidates(context.Background(), 0, PlatformOpenAI)
	require.NoError(t, err)
	require.Empty(t, candidates, "id<=0 → empty; no platform default seed")
}

func TestTkServableCandidateIDs_NewAPIDoesNotLeakClaude(t *testing.T) {
	ids := tkServableCandidateIDs(context.Background(), PlatformNewAPI, nil)
	require.Empty(t, ids, "newapi has no owned canonical list")
}
