//go:build unit

package service

// TK: See upstream Wei-Shaw/sub2api#2556.
//
// Regression tests for silent-refusal detection on the OpenAI Chat Completions
// raw passthrough. Each test isolates one branch of the predicate so a future
// refactor cannot silently re-introduce the ghost-stream-as-success bug.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ghostStreamBody reproduces the exact SSE shape captured in
// Wei-Shaw/sub2api#2556: role-only delta, empty content delta with
// finish_reason=stop, then [DONE]. No usage chunk.
const ghostStreamBody = `data: {"id":"resp_ghost","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"resp_ghost","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}

data: [DONE]

`

const normalStreamBody = `data: {"id":"resp_ok","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"resp_ok","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}

data: {"id":"resp_ok","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"resp_ok","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}

data: [DONE]

`

const toolCallsStreamBody = `data: {"id":"resp_tool","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"resp_tool","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup"}}]},"finish_reason":null}]}

data: {"id":"resp_tool","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":42,"completion_tokens":7}}

data: [DONE]

`

const reasoningOnlyStreamBody = `data: {"id":"resp_reasoning","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"resp_reasoning","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{"reasoning_content":"thinking..."},"finish_reason":null}]}

data: {"id":"resp_reasoning","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}

data: [DONE]

`

const lengthStopStreamBody = `data: {"id":"resp_len","object":"chat.completion.chunk","created":1,"model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}

data: [DONE]

`

func TestChatRawSilentRefusal_GhostStream_TriggersOpsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(ghostStreamBody)),
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.streamRawChatCompletions(
		c, resp, rawChatCompletionsTestAccount(),
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)

	events := opsUpstreamErrorsForTest(c)
	require.Len(t, events, 1, "ghost stream must record exactly one ops upstream error")
	require.Equal(t, silentRefusalKind, events[0].Kind)
	require.Equal(t, silentRefusalMessage, events[0].Message)
	require.Equal(t, rawChatCompletionsTestAccount().ID, events[0].AccountID)
}

func TestChatRawSilentRefusal_NormalStream_DoesNotTrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(normalStreamBody)),
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.streamRawChatCompletions(
		c, resp, rawChatCompletionsTestAccount(),
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(),
	)
	require.NoError(t, err)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Empty(t, opsUpstreamErrorsForTest(c), "normal stream must not record silent_refusal")
}

func TestChatRawSilentRefusal_ToolCallsStop_DoesNotTrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(toolCallsStreamBody)),
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.streamRawChatCompletions(
		c, resp, rawChatCompletionsTestAccount(),
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(),
	)
	require.NoError(t, err)
	require.Greater(t, result.Usage.OutputTokens, 0)
	require.Empty(t, opsUpstreamErrorsForTest(c), "tool_calls finish must not record silent_refusal")
}

func TestChatRawSilentRefusal_ReasoningStream_DoesNotTrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(reasoningOnlyStreamBody)),
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.streamRawChatCompletions(
		c, resp, rawChatCompletionsTestAccount(),
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(),
	)
	require.NoError(t, err)
	require.Greater(t, result.Usage.InputTokens, 0)
	require.Empty(t, opsUpstreamErrorsForTest(c), "reasoning-only stream with usage must not record silent_refusal")
}

func TestChatRawSilentRefusal_LengthStop_DoesNotTrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(lengthStopStreamBody)),
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.streamRawChatCompletions(
		c, resp, rawChatCompletionsTestAccount(),
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(),
	)
	require.NoError(t, err)
	require.Empty(t, opsUpstreamErrorsForTest(c), "finish_reason=length must not record silent_refusal")
}

func TestChatRawSilentRefusal_BufferedGhostResponse_TriggersOpsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ghostJSON := `{"id":"resp_ghost","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(ghostJSON)),
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.bufferRawChatCompletions(
		c, resp, rawChatCompletionsTestAccount(),
		"gpt-5.5", "gpt-5.5", "gpt-5.5", nil, nil, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	events := opsUpstreamErrorsForTest(c)
	require.Len(t, events, 1, "buffered ghost response must record exactly one ops upstream error")
	require.Equal(t, silentRefusalKind, events[0].Kind)
}

func TestChatRawSilentRefusalPredicate_RequiresZeroUsage(t *testing.T) {
	t.Parallel()
	// A response that looks empty but reports any tokens (e.g. real "model
	// had nothing to say but counted prompt tokens") must NOT be flagged.
	obs := chatRawStreamObservations{lastFinishReason: "stop"}
	usage := OpenAIUsage{InputTokens: 5}
	require.False(t, obs.IsSilentRefusal(usage))
}

func TestChatRawSilentRefusalPredicate_ChunkLevelErrorBlocks(t *testing.T) {
	t.Parallel()
	// An upstream that sends a chunk-level `error` field surfaced something
	// already — do not double-classify it as silent refusal.
	obs := chatRawStreamObservations{}
	obs.Observe(`{"error":{"message":"boom"}}`)
	require.True(t, obs.sawErrorEvent)
	require.False(t, obs.IsSilentRefusal(OpenAIUsage{}))
}

// opsUpstreamErrorsForTest reads the OpsUpstreamErrors slice that
// appendOpsUpstreamError writes to the gin context. Centralized so future
// refactors of the storage key only need to update one site.
func opsUpstreamErrorsForTest(c *gin.Context) []*OpsUpstreamErrorEvent {
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return nil
	}
	arr, _ := v.([]*OpsUpstreamErrorEvent)
	return arr
}
