package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUnsupportedOutputLimitsPreserveNestedData(t *testing.T) {
	body := []byte(`{"max_tokens":1,"max_output_tokens":2,"max_completion_tokens":3,"tools":[{"parameters":{"properties":{"max_tokens":{"type":"integer"}}}}],"input":{"max_output_tokens":9007199254740993}}`)
	normalized, changed, err := omitUnsupportedOutputLimitsJSON(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "9007199254740993", gjson.GetBytes(normalized, "input.max_output_tokens").Raw)
	require.Equal(t, "integer", gjson.GetBytes(normalized, "tools.0.parameters.properties.max_tokens.type").String())
	var decoded map[string]any
	require.NoError(t, decodeOpenAIJSONUseNumber(body, &decoded))
	require.True(t, omitUnsupportedOutputLimits(decoded))
	encoded, err := json.Marshal(decoded)
	require.NoError(t, err)
	require.JSONEq(t, string(normalized), string(encoded))
	for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
		require.False(t, gjson.GetBytes(normalized, field).Exists(), field)
	}
	again, changed, err := omitUnsupportedOutputLimitsJSON(normalized)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, normalized, again)
	require.False(t, omitUnsupportedOutputLimits(decoded))
}

func TestOutputLimitsAcrossCodexHTTPAndWebSocket(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		for _, path := range []string{"converted", "passthrough", "websocket"} {
			for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
				t.Run(accountType+"/"+path+"/"+field, func(t *testing.T) {
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
						Body: io.NopCloser(strings.NewReader(`{"output":[],"usage":{"input_tokens":1,"output_tokens":3}}`)),
					}}
					if accountType != AccountTypeAPIKey {
						upstream.resp.Header.Set("Content-Type", "text/event-stream")
						upstream.resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_limits\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":3}}}\n\n"))
					}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, Concurrency: 1,
						Credentials: map[string]any{"access_token": "test-token", "api_key": "test-key"},
						Extra:       map[string]any{"openai_passthrough": path == "passthrough"},
					}
					if accountType == AccountTypeAPIKey {
						account.Credentials["base_url"] = "https://api.openai.com"
					}
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.2","instructions":"Answer briefly","stream":false,"%s":1,"input":[{"role":"user","content":"hello"}]}`, field))
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					var sent []byte
					if path == "websocket" {
						var err error
						body = []byte(strings.Replace(string(body), `"stream":false`, `"type":"response.create"`, 1))
						sent, err = svc.tkPrepareWSIngressClientPayload(c, account, body, nil)
						require.NoError(t, err)
					} else {
						result, err := svc.Forward(t.Context(), c, account, body)
						require.NoError(t, err)
						require.NotNil(t, result)
						sent = upstream.lastBody
					}
					require.NotEmpty(t, sent)
					if accountType != AccountTypeAPIKey {
						for _, alias := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
							require.False(t, gjson.GetBytes(sent, alias).Exists(), "%s: %s", alias, sent)
						}
					} else if path == "converted" && field == "max_tokens" {
						require.Equal(t, int64(1), gjson.GetBytes(sent, "max_output_tokens").Int())
					} else if path != "converted" || field != "max_completion_tokens" {
						require.Equal(t, int64(1), gjson.GetBytes(sent, field).Int())
					}
				})
			}
		}
	}
}

func TestOutputLimitsCompatibilityLeavesOtherSuppliesUntouched(t *testing.T) {
	body := []byte(`{"max_tokens":8,"max_output_tokens":13,"max_completion_tokens":21,"input":"hello"}`)
	for _, platform := range []string{PlatformKiro, PlatformAntigravity, PlatformGrok, PlatformAnthropic} {
		t.Run(platform, func(t *testing.T) {
			normalized, changed, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(body, &Account{Platform: platform, Type: AccountTypeOAuth}, false)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, body, normalized)
		})
	}
}
