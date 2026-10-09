package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const searchHistoryCompactionRequest = `{"model":"gpt-5.5","stream":true,"instructions":"Summarize this synthetic history.","input":[{"type":"web_search_call","id":"ws_synthetic","status":"completed","action":{"type":"search","query":"weather"},"sequence":9007199254740993},{"role":"user","content":"Summarize."},{"type":"compaction_trigger"}],"tools":[]}`

func TestOAuthWebSearchHistoryPreservesNoToolsSemantics(t *testing.T) {
	for _, lite := range []bool{false, true} {
		t.Run(fmt.Sprintf("lite=%v", lite), func(t *testing.T) {
			body := []byte(searchHistoryCompactionRequest)
			next, changed, err := normalizeOpenAIOAuthWebSearchHistory(body, lite)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, "none", gjson.GetBytes(next, "tool_choice").String())
			require.Equal(t, gjson.GetBytes(body, "input.0").Raw, gjson.GetBytes(next, "input.0").Raw)
			items := gjson.GetBytes(next, "input").Array()
			require.Equal(t, "compaction_trigger", items[len(items)-1].Get("type").String())
			tool := gjson.GetBytes(next, "tools.0")
			if lite {
				require.Empty(t, gjson.GetBytes(next, "tools").Array())
				tool = items[len(items)-2].Get("tools.0")
				require.Equal(t, "additional_tools", items[len(items)-2].Get("type").String())
			}
			require.JSONEq(t, `{"type":"web_search","external_web_access":false}`, tool.Raw)
			again, changed, err := normalizeOpenAIOAuthWebSearchHistory(next, lite)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, next, again)
		})
	}
}

func TestOAuthWebSearchHistoryDoesNotBroadenToolPermissions(t *testing.T) {
	for _, fragment := range []string{
		`"tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"`,
		`"tools":[],"tool_choice":"required"`,
		`"tools":[],"tool_choice":{"type":"function","name":"lookup"}`,
		`"tools":{"invalid":"declaration"}`,
		`"tools":[{"type":"web_search_preview"}]`,
	} {
		body := []byte(`{"input":[{"type":"web_search_call"}],` + fragment + `}`)
		next, changed, err := normalizeOpenAIOAuthWebSearchHistory(body, false)
		require.NoError(t, err)
		require.False(t, changed, fragment)
		require.Equal(t, body, next)
	}
	for _, body := range []string{
		`{"input":[{"role":"user","content":"web_search_call"}],"tools":[]}`,
		`{"input":[{"type":"web_search_call"},{"type":"additional_tools","tools":[{"type":"function","name":"lookup"}]}]}`,
	} {
		next, changed, err := normalizeOpenAIOAuthWebSearchHistory([]byte(body), true)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, string(next))
	}
	// Explicit none preserves other declarations while preventing execution of
	// either the old tools or the newly supplied history declaration.
	body := []byte(`{"input":[{"type":"web_search_call"}],"tools":[{"type":"function","name":"lookup"}],"tool_choice":"none"}`)
	next, changed, err := normalizeOpenAIOAuthWebSearchHistory(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "lookup", gjson.GetBytes(next, "tools.0.name").String())
	require.Equal(t, "none", gjson.GetBytes(next, "tool_choice").String())
}

func TestOAuthWebSearchHistoryCompatibilityScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		typ     string
		compact bool
		want    bool
	}{
		{"OAuth WS", AccountTypeOAuth, false, true},
		{"API key", AccountTypeAPIKey, false, false},
		{"legacy compact", AccountTypeOAuth, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: tc.typ}
			next, _, err := normalizeOpenAIResponsesCompatibilityBody([]byte(searchHistoryCompactionRequest), account, false, tc.compact)
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(next, "tools.0.type").String() == "web_search")
		})
	}
}

func TestForwardOAuthWebSearchHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, lite := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%v/lite=%v", passthrough, lite), func(t *testing.T) {
				body := []byte(searchHistoryCompactionRequest)
				c := newOpenAICompactFallbackTestContext(t, "/v1/responses")
				if lite {
					c.Request.Header.Set(responsesLiteHeader, "true")
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")),
				}}
				account := openAICompatTestOAuthAccount(1, "history-test")
				account.Extra = map[string]any{"openai_passthrough": passthrough}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				result, err := svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 1)
				out := upstream.bodies[0]
				require.Equal(t, "none", gjson.GetBytes(out, "tool_choice").String())
				path := "tools.0.type"
				if lite {
					path = `input.#(type=="additional_tools").tools.0.type`
				}
				require.Equal(t, "web_search", gjson.GetBytes(out, path).String())
			})
		}
	}
}

func TestResponseProtectionStopsAccountAmplificationWithoutClient400(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"status":"failed","output":[],"error":{"type":"internal_error","code":"upstream_error","message":"response protection is unavailable"}}}`)
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable} {
		require.False(t, (&OpenAIGatewayService{}).shouldFailoverOpenAIUpstreamResponse(nil, status, "", payload))
		require.False(t, shouldFailoverOpenAIPassthroughResponse(nil, status, payload))
	}
	require.False(t, openAIStreamFailedEventShouldFailover(payload, ""))
	require.False(t, openAIStreamErrorEventShouldFailover(payload, ""))
	status, _ := openAIStreamFailedClientResponse(payload, "response protection is unavailable", "upstream_error")
	require.GreaterOrEqual(t, status, 500)
	// Merely mentioning the phrase in an echoed output must not suppress retries.
	echo := []byte(`{"response":{"output":[{"text":"response protection is unavailable"}],"error":{"message":"upstream processing failed","type":"internal_error"}}}`)
	require.True(t, openAIStreamFailedEventShouldFailover(echo, "upstream processing failed"))

	for _, passthrough := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%v/stream=%v", passthrough, stream), func(t *testing.T) {
				c := newOpenAICompactFallbackTestContext(t, "/v1/responses")
				body, err := sjson.SetBytes([]byte(searchHistoryCompactionRequest), "stream", stream)
				require.NoError(t, err)
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\n")),
				}}
				account := openAICompatTestOAuthAccount(1, "protection-test")
				account.Extra = map[string]any{"openai_passthrough": passthrough}
				_, err = (&OpenAIGatewayService{httpUpstream: upstream}).Forward(context.Background(), c, account, body)
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "terminal protection must not request another account")
				require.Len(t, upstream.bodies, 1)
			})
		}
	}
}
