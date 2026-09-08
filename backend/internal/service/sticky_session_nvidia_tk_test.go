//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/relaykit/types"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestNVIDIABuildChatDispatchOmitsPromptCacheKey(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })
	modelMapping := make(map[string]any, len(nvidiaBuildModelTargets))
	for model, target := range nvidiaBuildModelTargets {
		modelMapping[model] = target
	}
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL,
			"api_key": "test-key", "model_mapping": modelMapping},
	}
	for model, target := range nvidiaBuildModelMapping(account) {
		t.Run(model, func(t *testing.T) {
			called := false
			dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, in bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
				called = true
				require.Equal(t, newapiintegration.NVIDIABuildBaseURL, in.BaseURL)
				require.Equal(t, target, gjson.GetBytes(body, "model").String())
				require.False(t, gjson.GetBytes(body, "prompt_cache_key").Exists())
				return &bridge.DispatchOutcome{Model: model, UpstreamModel: target}, nil
			}
			c := newGinCtxWithAPIKey(t, &APIKey{ID: 9, Group: &Group{ID: 7}}, http.Header{})
			body := []byte(`{"messages":[{"role":"user","content":"Reply OK."}],"prompt_cache_key":"client-session"}`)
			body, err := sjson.SetBytes(body, "model", model)
			require.NoError(t, err)
			result, err := (&OpenAIGatewayService{}).ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
			require.NoError(t, err)
			require.True(t, called)
			require.Equal(t, target, result.UpstreamModel)
		})
	}
}

func TestNVIDIABuildStickyBodyCompatibility(t *testing.T) {
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	for _, mode := range []StickyMode{StickyModeAuto, StickyModePassthrough, StickyModeOff} {
		for _, clientKey := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/client-key=%t/stream=%t", mode, clientKey, stream), func(t *testing.T) {
					c := newGinCtxWithAPIKey(t, &APIKey{ID: 9, Group: &Group{ID: 7, StickyRoutingMode: string(mode)}}, http.Header{})
					body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"Reply OK."}],"tools":[{"type":"function","function":{"name":"ping","parameters":{"type":"object"}}}],"reasoning_effort":"low"}`)
					body, err := sjson.SetBytes(body, "stream", stream)
					require.NoError(t, err)
					want := string(body)
					if clientKey {
						body, err = sjson.SetBytes(body, "prompt_cache_key", "client-session")
						require.NoError(t, err)
					}
					out := applyStickyToNewAPIBridge(context.Background(), c, nil, account, body, "")
					require.False(t, gjson.GetBytes(out, "prompt_cache_key").Exists())
					require.JSONEq(t, want, string(out), "preserve the model and request payload")
					if clientKey && mode != StickyModeOff {
						require.Equal(t, "client-session", c.Request.Header.Get("X-Session-Id"))
					}
				})
			}
		}
	}
}

func TestNVIDIABuildStickyCompatibilityLeavesOtherChannelsUnchanged(t *testing.T) {
	for _, test := range []struct {
		name, platform, base string
		channelType          int
	}{
		{"other-host", PlatformNewAPI, "https://api.openai.com", newapiconstant.ChannelTypeOpenAI},
		{"host-suffix", PlatformNewAPI, "https://integrate.api.nvidia.com.example", newapiconstant.ChannelTypeOpenAI},
		{"other-platform", PlatformOpenAI, newapiintegration.NVIDIABuildBaseURL, newapiconstant.ChannelTypeOpenAI},
		{"other-channel", PlatformNewAPI, newapiintegration.NVIDIABuildBaseURL, newapiconstant.ChannelTypeDeepSeek},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{Platform: test.platform, Type: AccountTypeAPIKey, ChannelType: test.channelType,
				Credentials: map[string]any{"base_url": test.base}}
			c := newGinCtxWithAPIKey(t, &APIKey{ID: 9, Group: &Group{ID: 7}}, http.Header{})
			body := []byte(`{"model":"kimi-k3","messages":[],"prompt_cache_key":"client-session"}`)
			out := applyStickyToNewAPIBridge(context.Background(), c, nil, account, body, "")
			require.JSONEq(t, string(body), string(out))
			require.Equal(t, "client-session", c.Request.Header.Get("X-Session-Id"))
		})
	}
}
