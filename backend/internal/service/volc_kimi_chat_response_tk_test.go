//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestVolcKimiMirrorOuterCachedTokens(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()

	hit := []byte(`{"usage":{"prompt_tokens":1910,"completion_tokens":16,"total_tokens":1926,"prompt_tokens_details":{"cached_tokens":1792}}}`)
	got := applyVolcKimiRawChatCompletionsResponse(account, "kimi-k3", hit)
	require.EqualValues(t, 1792, gjson.GetBytes(got, "usage.cached_tokens").Int())
	require.EqualValues(t, 1792, gjson.GetBytes(got, "usage.prompt_tokens_details.cached_tokens").Int())
	require.EqualValues(t, 1910, gjson.GetBytes(got, "usage.prompt_tokens").Int())

	miss := []byte(`{"usage":{"prompt_tokens":1910,"completion_tokens":16,"total_tokens":1926,"prompt_tokens_details":{"cached_tokens":0}}}`)
	got = applyVolcKimiRawChatCompletionsResponse(account, "kimi-k3", miss)
	require.True(t, gjson.GetBytes(got, "usage.cached_tokens").Exists())
	require.EqualValues(t, 0, gjson.GetBytes(got, "usage.cached_tokens").Int())

	already := []byte(`{"usage":{"prompt_tokens":10,"cached_tokens":3,"prompt_tokens_details":{"cached_tokens":7}}}`)
	got = applyVolcKimiRawChatCompletionsResponse(account, "kimi-k3", already)
	require.EqualValues(t, 3, gjson.GetBytes(got, "usage.cached_tokens").Int())

	other := applyVolcKimiRawChatCompletionsResponse(nil, "kimi-k3", hit)
	require.False(t, gjson.GetBytes(other, "usage.cached_tokens").Exists())
	nonKimi := applyVolcKimiRawChatCompletionsResponse(account, "glm-5.3", hit)
	require.False(t, gjson.GetBytes(nonKimi, "usage.cached_tokens").Exists())
}

func TestVolcKimiStreamFinishReasonNullAndCachedTokens(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()

	delta := `data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"1、2"}}]}`
	got := applyVolcKimiRawChatCompletionsSSELine(account, "kimi-k3", delta)
	payload, ok := extractOpenAISSEDataLine(got)
	require.True(t, ok)
	require.True(t, gjson.Get(payload, "choices.0.finish_reason").Exists())
	require.Equal(t, gjson.Null, gjson.Get(payload, "choices.0.finish_reason").Type)
	require.Equal(t, "1、2", gjson.Get(payload, "choices.0.delta.content").String())

	stop := `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	got = applyVolcKimiRawChatCompletionsSSELine(account, "kimi-k3", stop)
	require.Equal(t, stop, got)

	usage := `data: {"id":"x","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":30,"total_tokens":50,"prompt_tokens_details":{"cached_tokens":4}}}`
	got = applyVolcKimiRawChatCompletionsSSELine(account, "kimi-k3", usage)
	payload, ok = extractOpenAISSEDataLine(got)
	require.True(t, ok)
	require.EqualValues(t, 4, gjson.Get(payload, "usage.cached_tokens").Int())

	require.Equal(t, delta, applyVolcKimiRawChatCompletionsSSELine(nil, "kimi-k3", delta))
	require.Equal(t, "data: [DONE]", applyVolcKimiRawChatCompletionsSSELine(account, "kimi-k3", "data: [DONE]"))
}
