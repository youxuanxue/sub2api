//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCanonicalGeminiServableTrafficModel(t *testing.T) {
	require.NotEmpty(t, geminiTrafficAliases)
	for alias, target := range geminiTrafficAliases {
		require.NotEqual(t, alias, target, alias)
		require.Equal(t, target, canonicalGeminiServableTrafficModel(alias))
		require.Equal(t, target, canonicalGeminiServableTrafficModel("models/"+alias))
		for _, platform := range []string{PlatformGemini, PlatformAntigravity, PlatformNewAPI, PlatformOpenAI} {
			require.Equal(t, target, normalizeRequestedModelForLookup(platform, alias), platform)
		}
		// Unmatched allowlists keep the client id. Lookup gains the target as
		// an extra key; it does not rewrite OpenAI passthrough.
		openai := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"},
		}}
		got, matched := openai.ResolveMappedModel(alias)
		require.False(t, matched)
		require.Equal(t, alias, got)
	}
	const unknown = "gemini-not-a-catalog-model"
	require.Equal(t, unknown, canonicalGeminiServableTrafficModel(unknown))
	require.Equal(t, unknown, normalizeRequestedModelForLookup(PlatformGemini, unknown))
}

func TestGeminiServableTrafficAliasRoutesWithdrawnPreview(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	vertexKey := strconv.Itoa(newapiconstant.ChannelTypeVertexAi)
	vertexFloor := floor.NewAPIChannelTypes[vertexKey]
	agFloor := floor.Platforms[PlatformAntigravity]
	require.NotEmpty(t, vertexFloor)
	require.NotEmpty(t, agFloor)

	for alias, target := range geminiTrafficAliases {
		t.Run(alias, func(t *testing.T) {
			require.Equal(t, target, vertexFloor[target])
			require.NotContains(t, vertexFloor, alias)
			require.Equal(t, target, agFloor[target])
			require.NotContains(t, agFloor, alias)

			resolver, gateway, accounts, _ := candidateGoogleFixture(t)
			accounts[0].Credentials["model_mapping"] = modelMappingToAny(vertexFloor)
			accounts[1].Credentials["model_mapping"] = modelMappingToAny(agFloor)
			wireCandidateTestResolver(resolver, gateway, accounts)

			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"OK"}]}],"tools":[{"googleSearch":{}}]}`)
			ctx := resolver.WithRequest(context.Background(), ShapeGemini, "/v1beta/models/"+alias+":generateContent", alias, body)
			for i := range accounts {
				plan, governed, err := protocolPlanForAccount(ctx, &accounts[i], alias)
				require.True(t, governed)
				require.NoError(t, err, "account %d", accounts[i].ID)
				require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())
				require.Equal(t, target, plan.ResolvedModel())
				require.NotContains(t, plan.Endpoint(), alias)
				require.Contains(t, plan.Endpoint(), target)
			}
			group, err := resolver.Resolve(ctx, universalKey(334), ShapeGemini, alias, "")
			require.NoError(t, err)
			require.NotNil(t, group)
		})
	}
}

func TestGeminiServableTrafficAliasLeavesUnknownModelUnsupported(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	vertexKey := strconv.Itoa(newapiconstant.ChannelTypeVertexAi)
	resolver, gateway, accounts, _ := candidateGoogleFixture(t)
	accounts[0].Credentials["model_mapping"] = modelMappingToAny(floor.NewAPIChannelTypes[vertexKey])
	accounts[1].Credentials["model_mapping"] = modelMappingToAny(floor.Platforms[PlatformAntigravity])
	wireCandidateTestResolver(resolver, gateway, accounts)

	const unknown = "gemini-not-a-catalog-model"
	require.Equal(t, unknown, canonicalGeminiServableTrafficModel(unknown))
	ctx := resolver.WithRequest(context.Background(), ShapeGemini, "/v1beta/models/"+unknown+":generateContent", unknown,
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`))
	for i := range accounts {
		_, governed, err := protocolPlanForAccount(ctx, &accounts[i], unknown)
		require.True(t, governed)
		require.ErrorIs(t, err, protocolrouter.ErrModelNotAllowed, "account %d", accounts[i].ID)
	}
	group, err := resolver.Resolve(ctx, universalKey(334), ShapeGemini, unknown, "")
	require.Nil(t, group)
	require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
	require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
}
