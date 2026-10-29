//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCandidateMappedModelWithoutPreservingRouteIsCapacity(t *testing.T) {
	const model = "qwen3.8-flash"
	body := []byte(`{"model":"qwen3.8-flash","messages":[{"role":"user","content":"OK"}],"thinking":{"type":"disabled"}}`)
	for _, native := range []bool{false, true} {
		a := *protocolRoutingOpenAIAccount(10, "responses")
		a.GroupIDs = []int64{10}
		a.Credentials["model_mapping"] = map[string]any{model: model}
		if native {
			attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses)
		}
		peer := globalCandidateAccount(11, 1, 10)
		peer.Credentials["model_mapping"] = map[string]any{"other-model": "other-model"}
		r, _, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false)}, []Account{a, peer})
		_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", model, body, "", "")
		if native {
			require.NoError(t, err)
			require.Equal(t, int64(10), state.current.account.ID)
			require.Equal(t, protocolrouter.ProtocolChatCompletions, state.current.plan.TargetProtocol())
		} else {
			require.ErrorIs(t, err, ErrUniversalUnsupportedRequest)
			require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
			require.NotErrorIs(t, err, ErrUniversalUnsupportedModel)
		}
		_, _, err = r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "missing-model", []byte(`{"model":"missing-model","messages":[{"role":"user","content":"OK"}]}`), "", "")
		require.ErrorIs(t, err, ErrUniversalUnsupportedModel, "a real model-policy miss keeps its existing contract")
	}
}
