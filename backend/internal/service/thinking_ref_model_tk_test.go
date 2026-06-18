package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// unsignedThinkingBody is an Anthropic /v1/messages body carrying an assistant
// thinking block with a bad signature — the shape that anthropic-strict upstream
// rejects with a signature 400, and that the retry filter rewrites to text.
const unsignedThinkingBody = `{
	"model":"claude-sonnet-4-6",
	"thinking":{"type":"enabled","budget_tokens":1024},
	"messages":[
		{"role":"user","content":[{"type":"text","text":"Hi"}]},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"Let me think...","signature":"bad_sig"},
			{"type":"text","text":"Answer"}
		]}
	]
}`

func topLevelThinkingPresent(t *testing.T, body []byte) bool {
	t.Helper()
	var req map[string]any
	require.NoError(t, json.Unmarshal(body, &req))
	_, has := req["thinking"]
	return has
}

// TestThinkingRefModel_AnthropicRelayUsesMappedModel pins the NATIVE relay path
// invariant: the thinking helpers must key on the MAPPED upstream model. The
// classic trap is an account mapping claude-sonnet-4-6 → a passback-required
// upstream (deepseek): keying on the mapped model correctly SKIPS the strip
// (preserving the passback contract), whereas keying on the original claude
// model would wrongly strip and break that upstream.
//
// This is a relay-invariant registered in scripts/sentinels/relay-invariants.json.
func TestThinkingRefModel_AnthropicRelayUsesMappedModel(t *testing.T) {
	const originalModel = "claude-sonnet-4-6"
	const mappedModel = "deepseek-v4-pro" // account model_mapping target

	// The helper selects the mapped model verbatim.
	require.Equal(t, mappedModel, thinkingRefModelForAnthropicRelay(mappedModel))

	// Why the choice matters: the two models classify into OPPOSITE protocols.
	require.Equal(t, ThinkingProtocolPassbackRequired, ResolveThinkingProtocol(mappedModel),
		"mapped deepseek upstream is passback-required — thinking blocks must NOT be stripped")
	require.Equal(t, ThinkingProtocolAnthropicStrict, ResolveThinkingProtocol(originalModel),
		"original claude model is anthropic-strict — keying on it here would WRONGLY strip")

	// Behavioral proof: keyed on the mapped model the retry filter is a no-op
	// (passback preserved); keyed on the original model it would mutate the body.
	relayRef := thinkingRefModelForAnthropicRelay(mappedModel)
	keptForPassback := FilterThinkingBlocksForRetry([]byte(unsignedThinkingBody), relayRef)
	require.Equal(t, unsignedThinkingBody, string(keptForPassback),
		"mapped passback-required model must leave the body untouched")
	require.True(t, topLevelThinkingPresent(t, keptForPassback),
		"top-level thinking must survive on the passback path")

	wouldStripIfInverted := FilterThinkingBlocksForRetry([]byte(unsignedThinkingBody), originalModel)
	require.NotEqual(t, unsignedThinkingBody, string(wouldStripIfInverted),
		"sanity: passing the ORIGINAL model (the inversion bug) would have mutated the body — "+
			"this is exactly what keying on the mapped model prevents")
}

// TestThinkingRefModel_AnthropicCompatUsesOriginalModel pins the Gemini-compat
// path invariant (Anthropic-shaped client → Gemini backend): the thinking
// helpers must key on the ORIGINAL client model. The mapped model here is a
// Gemini model that ResolveThinkingProtocol classifies as non-strict, so keying
// on it would SKIP the very strip the Anthropic-dialect body needs, leaving an
// unsigned thinking block that the upstream rejects with a 400.
//
// This is a relay-invariant registered in scripts/sentinels/relay-invariants.json.
func TestThinkingRefModel_AnthropicCompatUsesOriginalModel(t *testing.T) {
	const originalModel = "claude-sonnet-4-6" // client's Anthropic model
	const mappedModel = "gemini-2.5-pro"      // actual upstream backend

	// The helper selects the original client model verbatim.
	require.Equal(t, originalModel, thinkingRefModelForAnthropicCompat(originalModel))

	// Why the choice matters: only the original model is anthropic-strict; the
	// mapped Gemini model is NOT, so keying on it would skip required filtering.
	require.Equal(t, ThinkingProtocolAnthropicStrict, ResolveThinkingProtocol(originalModel),
		"original claude model is anthropic-strict — the unsigned thinking block must be stripped")
	require.NotEqual(t, ThinkingProtocolAnthropicStrict, ResolveThinkingProtocol(mappedModel),
		"mapped gemini model is non-strict — keying on it would SKIP the strip and 400 upstream")

	// Behavioral proof: keyed on the original model the retry filter rewrites the
	// thinking block to text and drops top-level thinking; keyed on the mapped
	// gemini model it would be a no-op (the inversion bug).
	compatRef := thinkingRefModelForAnthropicCompat(originalModel)
	stripped := FilterThinkingBlocksForRetry([]byte(unsignedThinkingBody), compatRef)
	require.NotEqual(t, unsignedThinkingBody, string(stripped),
		"original anthropic-strict model must rewrite the unsigned thinking block")
	require.False(t, topLevelThinkingPresent(t, stripped),
		"top-level thinking must be disabled on the compat strip path")

	wouldNoOpIfInverted := FilterThinkingBlocksForRetry([]byte(unsignedThinkingBody), mappedModel)
	require.Equal(t, unsignedThinkingBody, string(wouldNoOpIfInverted),
		"sanity: passing the MAPPED gemini model (the inversion bug) would have left the "+
			"unsigned thinking block in place — exactly the 400 keying on originalModel prevents")
}
