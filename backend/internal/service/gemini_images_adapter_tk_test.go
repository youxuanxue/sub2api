//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func geminiImagesTestAccount() Account {
	account := Account{ID: 850, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{740}, Credentials: map[string]any{"access_token": "test-only", "project_id": "test-project", "plan_type": "Pro", "model_mapping": map[string]any{"nano-2": "gemini-3.1-flash-image"}}}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolGeminiGenerateContent)
	return account
}
func TestGeminiImagesCandidateUsesNativePlan(t *testing.T) {
	for _, direct := range []bool{false, true} {
		group := grp(740, PlatformNewAPI, 1, false)
		group.AllowImageGeneration = true
		account := geminiImagesTestAccount()
		resolver, _, key := globalCandidateFixture([]Group{group}, []Account{account})
		if direct {
			key.RoutingMode = RoutingModeDirect
			key.Group = &group
			key.GroupID = &group.ID
		}
		body := []byte(`{"model":"nano-2","prompt":"blue cup","size":"4K","aspect_ratio":"16:9"}`)
		ctx, state, err := resolver.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIImages, "/v1/images/generations", "nano-2", body, "", "")
		require.NoError(t, err)
		require.NotNil(t, state.current.plan)
		require.Equal(t, account.ID, state.current.account.ID)
		require.Equal(t, protocolrouter.AdapterGeminiIdentity, state.current.plan.AdapterID())
		request, ok := ProtocolRoutingRequest(state.current.ctx)
		require.True(t, ok)
		require.Equal(t, "4K", gjson.GetBytes(request.Body(), "generationConfig.imageConfig.imageSize").String())
		require.Equal(t, "16:9", gjson.GetBytes(request.Body(), "generationConfig.imageConfig.aspectRatio").String())
		_, native, err := PrepareGeminiImagesRequest(body)
		require.NoError(t, err)
		expected, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolGeminiGenerateContent, "", "nano-2", false, native)
		require.NoError(t, err)
		require.Equal(t, expected.Digest(), request.Digest())
		selection, err := resolver.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "nano-2", nil, "", key.UserID)
		require.NoError(t, err)
		defer selection.ReleaseFunc()
		require.Equal(t, account.ID, selection.Account.ID)
	}
}
func TestGeminiImagesCandidateRejectsUnauthorizedOrLossyRequests(t *testing.T) {
	for _, kind := range []string{"image_disabled", "wrong_group", "web_only", "multiple", "missing_capability"} {
		t.Run(kind, func(t *testing.T) {
			group := grp(740, PlatformAntigravity, 1, false)
			group.AllowImageGeneration = true
			account := geminiImagesTestAccount()
			body := []byte(`{"model":"nano-2","prompt":"cup"}`)
			switch kind {
			case "image_disabled":
				group.AllowImageGeneration = false
			case "wrong_group":
				account.GroupIDs = []int64{741}
			case "web_only":
				account = webCandidateAccount(true)
			case "multiple":
				body = []byte(`{"model":"nano-2","prompt":"cup","n":2}`)
			case "missing_capability":
				attachTestProtocolCapability(&account, protocolrouter.ProtocolChatCompletions)
			}
			resolver, _, key := globalCandidateFixture([]Group{group}, []Account{account})
			key.RoutingMode = RoutingModeDirect
			key.Group = &group
			key.GroupID = &group.ID
			_, _, err := resolver.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIImages, "/v1/images/generations", "nano-2", body, "", "")
			require.Error(t, err)
		})
	}
}
func TestGeminiImagesDiscoveryProjectsAdmittedProfile(t *testing.T) {
	group := grp(740, PlatformAntigravity, 1, false)
	group.AllowImageGeneration = true
	svc, key := candidateDiscoveryFixture([]Group{group}, []Account{geminiImagesTestAccount()})
	models, err := svc.List(context.Background(), key, UniversalProtocolOpenAI)
	require.NoError(t, err)
	found := false
	for _, model := range models {
		if model.ID != "nano-2" {
			continue
		}
		for _, profile := range model.ImageGeneration {
			if profile.Endpoint != "/v1/images/generations" {
				continue
			}
			found = true
			require.Equal(t, []int{1}, profile.Counts)
			require.False(t, profile.InputImage)
			require.ElementsMatch(t, geminiImageDiscoveryRatios, profile.AspectRatios)
			require.True(t, profile.SoftAspectRatio)
		}
	}
	require.True(t, found, "authorized Images profile must be discoverable")
}

func TestGeminiImagesPreservesExistingNewAPIProvider(t *testing.T) {
	group := grp(740, PlatformNewAPI, 1, false)
	group.AllowImageGeneration = true
	account := globalCandidateAccount(851, 0, group.ID)
	account.Platform = PlatformNewAPI
	account.ChannelType = newapiconstant.ChannelTypeOpenAI
	account.Credentials["model_mapping"] = map[string]any{"nano-2": "gemini-3.1-flash-image"}
	for _, extra := range []string{"", `,"n":2,"quality":"high"`} {
		resolver, _, key := globalCandidateFixture([]Group{group}, []Account{account, geminiImagesTestAccount()})
		body := []byte(`{"model":"nano-2","prompt":"cup"` + extra + `}`)
		ctx := resolver.WithRequest(context.Background(), ShapeOpenAIImages, "/v1/images/generations", "nano-2", body)
		_, state, err := resolver.PrepareCandidateRequest(ctx, key, ShapeOpenAIImages, "/v1/images/generations", "nano-2", body, "", PlatformNewAPI)
		require.NoError(t, err)
		require.Equal(t, account.ID, state.current.account.ID)
		require.Nil(t, state.current.plan, "existing Images provider must not execute AG native Gemini")
		require.JSONEq(t, string(body), string(state.body), "provider-specific options remain intact")
	}
}
