//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/stretchr/testify/require"
)

// cursorContentSupportedPerModel is the pre-split shape: one full projection per
// model, with the model threaded all the way into the filter. The split path must
// agree with it for every body and model, otherwise a cached wire would answer
// for a model it was not derived against.
func cursorContentSupportedPerModel(request protocolrouter.CanonicalRequest, resolvedModel string) bool {
	wire, ok := cursorExecutionWire(request)
	if !ok {
		return false
	}
	return cursor.ValidateMessagesContent(FilterWebSearchHistoryBlocks(wire, resolvedModel)) == nil
}

func TestCursorContentSplitMatchesPerModelPath(t *testing.T) {
	// Models spanning both stripAll values, including several that agree on it,
	// so a key collapse that lost a real distinction would show up here.
	models := []string{
		"claude-sonnet-4-6", "claude-opus-4-1", "totally-unknown-model",
		"deepseek-v3.2", "glm-4.7", "kimi-k2", "minimax-m2.5",
	}
	bodies := map[string]string{
		"emulated_websearch": emulatedWebSearchBody,
		"genuine_websearch":  genuineWebSearchBody,
		"plain_text":         `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`,
		"empty_text_blocks":  `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]}]}`,
		"tool_result":        `{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}]}`,
		"invalid_messages":   `{"model":"claude-sonnet-4-6","messages":"not-an-array"}`,
	}

	for name, raw := range bodies {
		for _, inbound := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions} {
			t.Run(fmt.Sprintf("%s/%s", name, inbound), func(t *testing.T) {
				request, err := protocolrouter.ParseCanonicalRequest(inbound, protocolrouter.ResponsesPathNone, "claude-sonnet-4-6", false, []byte(raw))
				require.NoError(t, err)
				// One cache across every model, exactly as a fan-out caller uses it.
				cache := &cursorRequestContentCache{}
				for _, model := range models {
					want := cursorContentSupportedPerModel(request, model)
					require.Equal(t, want, cursorProtocolContentSupported(request, model),
						"uncached split disagrees with per-model path for %q", model)
					require.Equal(t, want, cache.supported(request, model),
						"cached split disagrees with per-model path for %q", model)
				}
			})
		}
	}
}

// A cached wire must never answer for a different request. The cache is
// per-request by scope, but the digest key is what actually enforces it.
func TestCursorContentCacheSeparatesRequestsAndStripDecisions(t *testing.T) {
	const model = "claude-sonnet-4-6"
	genuine, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, []byte(genuineWebSearchBody))
	require.NoError(t, err)
	cache := &cursorRequestContentCache{}

	// deepseek strips every web-search block, claude keeps the genuine ones, so
	// the two must not share an outcome even though they share one wire.
	require.True(t, cache.supported(genuine, "deepseek-v3.2"))
	require.False(t, cache.supported(genuine, model), "differing strip decisions cannot borrow each other's outcome")
	require.Len(t, cache.wires, 1, "one body must be projected once, not once per model")

	text, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.True(t, cache.supported(text, model), "a different request cannot inherit a cached rejection")
	require.Len(t, cache.wires, 2, "a second body needs its own projection")
}

// Models that agree on the strip decision must share one projection, which is
// the whole point of keying on it instead of on the model string.
func TestCursorContentCacheSharesProjectionAcrossAgreeingModels(t *testing.T) {
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, "claude-sonnet-4-6", false, []byte(genuineWebSearchBody))
	require.NoError(t, err)
	cache := &cursorRequestContentCache{}
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-1", "claude-haiku-4-5", "totally-unknown-model"} {
		require.False(t, WebSearchHistoryStripsAllBlocks(model), "fixture expects anthropic-strict models")
		cache.supported(request, model)
	}
	require.Len(t, cache.outcomes, 1, "models agreeing on the strip decision must share one outcome")
	require.Len(t, cache.wires, 1, "models agreeing on the strip decision must share one projection")
}
