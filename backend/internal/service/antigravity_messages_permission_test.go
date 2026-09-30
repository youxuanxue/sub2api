//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAntigravityMessagesPermissionAdminLifecycle(t *testing.T) {
	repo := &groupPlatformRepoStub{group: &Group{ID: 21, Name: "Google-Antigravity", Platform: PlatformAntigravity, AllowImageGeneration: true}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{groupRepo: repo, authCacheInvalidator: cache}
	for _, enabled := range []bool{true, false} {
		got, err := svc.UpdateGroup(context.Background(), 21, &UpdateGroupInput{AllowMessagesDispatch: &enabled})
		require.NoError(t, err)
		require.Equal(t, enabled, got.AllowMessagesDispatch)
		require.Equal(t, enabled, repo.updated.AllowMessagesDispatch)
		require.True(t, got.AllowImageGeneration)
		repo.group = repo.updated
		description := "updated description"
		got, err = svc.UpdateGroup(context.Background(), 21, &UpdateGroupInput{Description: &description})
		require.NoError(t, err)
		require.Equal(t, enabled, got.AllowMessagesDispatch)
		repo.group = repo.updated
	}
	require.Equal(t, []int64{21, 21, 21, 21}, cache.groupIDs)
}

func TestAntigravityMessagesPermissionEndpointIsolation(t *testing.T) {
	const model = "gemini-3.8-flash"
	cases := []struct {
		name, path, model, body string
		shape                   UniversalShape
		needsPermission         bool
	}{
		{"messages", "/v1/messages", model, `{"model":"gemini-3.8-flash","max_tokens":64,"messages":[{"role":"user","content":"OK"}]}`, ShapeAnthropicMessages, true},
		{"count_tokens", "/v1/messages/count_tokens", model, `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"OK"}]}`, ShapeAnthropicCountTokens, false},
		{"chat", "/v1/chat/completions", model, `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"OK"}]}`, ShapeOpenAIChat, false},
		{"responses", "/v1/responses", model, `{"model":"gemini-3.8-flash","input":"OK"}`, ShapeOpenAIChat, false},
		{"gemini", "/v1beta/models/gemini-3.8-flash:generateContent", model, `{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`, ShapeGemini, false},
		{"images", "/v1/images/generations", "nano-2", `{"model":"nano-2","prompt":"blue cup"}`, ShapeOpenAIImages, false},
	}
	for _, direct := range []bool{true, false} {
		for _, enabled := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/direct=%v/enabled=%v", tc.name, direct, enabled), func(t *testing.T) {
					group := grp(740, PlatformAntigravity, 1, false)
					group.AllowImageGeneration = true
					group.AllowMessagesDispatch = enabled
					sanitizeGroupMessagesDispatchFields(&group)
					account := geminiImagesTestAccount()
					account.Credentials["model_mapping"].(map[string]any)[model] = model
					resolver, _, key := globalCandidateFixture([]Group{group}, []Account{account})
					if direct {
						key.RoutingMode = RoutingModeDirect
						key.Group = &group
						key.GroupID = &group.ID
					}
					_, state, err := resolver.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, tc.model, []byte(tc.body), "", "")
					if tc.needsPermission && !enabled {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					require.Equal(t, account.ID, state.current.account.ID)
					if tc.shape == ShapeAnthropicCountTokens {
						// Token counting retains its existing non-Plan handler/fallback path.
						require.Nil(t, state.current.plan)
					} else {
						require.Equal(t, account.Credentials["model_mapping"].(map[string]any)[tc.model], state.current.plan.ResolvedModel())
					}
				})
			}
		}
	}
}
