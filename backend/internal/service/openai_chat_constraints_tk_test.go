//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapidto "github.com/QuantumNous/new-api/dto"
	newapitypes "github.com/QuantumNous/new-api/types"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const constraintTools = `[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{},"additionalProperties":false},"strict":true}},{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}]`
const constraintFunctions = `[{"name":"get_weather","parameters":{"type":"object"}},{"name":"get_time","parameters":{"type":"object"}}]`

func TestChatToolConstraints_SelectionAndHistory(t *testing.T) {
	const history = `[{"role":"system","content":"rules"},{"role":"assistant","content":null,"tool_calls":[{"id":"old","type":"function","function":{"name":"get_time","arguments":"{}"}}]},{"role":"tool","tool_call_id":"old","content":"12:00"},{"role":"user","content":"weather now"}]`
	for _, tc := range []struct {
		name, selection string
		none            bool
	}{
		{"modern named", `"tool_choice":{"type":"function","function":{"name":"get_weather"}}`, false},
		{"legacy named", `"function_call":{"name":"get_weather"}`, false},
		{"modern none", `"tool_choice":"none","function_call":"auto"`, true},
		{"legacy none", `"function_call":"none","tool_choice":"auto"`, true},
		{"both named", `"tool_choice":{"type":"function","function":{"name":"get_weather"}},"function_call":{"name":"get_weather"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"alias","messages":` + history + `,"metadata":{"large":9007199254740993},"response_format":{"type":"json_schema","json_schema":{"strict":true,"name":"test","schema":{"type":"object"}}},"tools":` + constraintTools + `,"functions":` + constraintFunctions + `,` + tc.selection + `}`)
			original := bytes.Clone(body)
			got, err := normalizeChatToolConstraints(body)
			require.NoError(t, err)
			require.Equal(t, original, body)
			require.JSONEq(t, history, gjson.GetBytes(got, "messages").Raw)
			require.Equal(t, "9007199254740993", gjson.GetBytes(got, "metadata.large").Raw)
			require.JSONEq(t, gjson.GetBytes(body, "response_format").Raw, gjson.GetBytes(got, "response_format").Raw)
			if tc.none {
				require.False(t, gjson.GetBytes(got, "tools").Exists())
				require.False(t, gjson.GetBytes(got, "functions").Exists())
				require.Equal(t, "none", gjson.GetBytes(got, "tool_choice").String())
				require.Equal(t, "none", gjson.GetBytes(got, "function_call").String())
			} else {
				require.Equal(t, int64(1), gjson.GetBytes(got, "tools.#").Int())
				require.Equal(t, int64(1), gjson.GetBytes(got, "functions.#").Int())
				require.Equal(t, "get_weather", gjson.GetBytes(got, "tools.0.function.name").String())
				require.Equal(t, "get_weather", gjson.GetBytes(got, "functions.0.name").String())
				require.JSONEq(t, gjson.Get(constraintTools, "0").Raw, gjson.GetBytes(got, "tools.0").Raw)
			}
			again, err := normalizeChatToolConstraints(got)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
}

func TestChatToolConstraints_InvalidAndUnconstrained(t *testing.T) {
	for _, body := range []string{
		`null`, `{`,
		`{"tool_choice":"none","function_call":{"name":"secret"}}`,
		`{"function_call":"none","tool_choice":"required"}`,
		`{"tool_choice":{"type":"function","function":{"name":"a"}},"function_call":{"name":"b"}}`,
		`{"tool_choice":{"type":"function"}}`,
		`{"tool_choice":5}`,
		`{"tool_choice":{"type":"function","function":{"name":"missing"}},"tools":` + constraintTools + `}`,
		`{"function_call":{"name":"get_weather"},"functions":[{"name":"get_weather"},{"name":"get_weather"}]}`,
		`{"function_call":{"name":"secret"},"functions":{}}`,
	} {
		t.Run(body, func(t *testing.T) {
			got, err := normalizeChatToolConstraints([]byte(body))
			require.Error(t, err)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), "secret")
		})
	}
	for _, selection := range []string{``, `,"tool_choice":"auto"`, `,"tool_choice":"required"`, `,"tool_choice":null`, `,"function_call":"auto"`} {
		body := []byte(`{ "tools": ` + constraintTools + selection + ` }`)
		got, err := normalizeChatToolConstraints(body)
		require.NoError(t, err)
		require.Equal(t, body, got)
	}
}

func TestChatProviderConstraints_DestinationAndRetryIsolation(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"developer","content":"rules"}],"tools":` + constraintTools + `,"tool_choice":"none"}`)
	original := bytes.Clone(body)
	account := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{"base_url": newapiintegration.AliTokenPlanBaseURL}}
	for _, endpoint := range []string{
		"https://api.openai.com/v1/chat/completions",
		"https://token-plan.cn-beijing.maas.aliyuncs.com.example.test/compatible-mode/v1/chat/completions",
		"https://example.test/token-plan.cn-beijing.maas.aliyuncs.com/chat/completions",
		"https://token-plan.cn-beijing.maas.aliyuncs.com@example.test/v1/chat/completions",
		"https://ark.cn-beijing.volces.com/api/v3/chat/completions",
	} {
		got, err := normalizeChatProviderRequest(account, endpoint, body)
		require.NoError(t, err)
		require.Equal(t, body, got, "selected destination, not account name/base/model, controls adaptation")
	}
	got, err := normalizeChatProviderRequest(account, newapiintegration.AliTokenPlanBaseURL+"/compatible-mode/v1/chat/completions", body)
	require.NoError(t, err)
	require.Equal(t, "system", gjson.GetBytes(got, "messages.0.role").String())
	require.False(t, gjson.GetBytes(got, "tools").Exists())
	got, err = normalizeChatProviderRequest(account, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", body)
	require.NoError(t, err)
	require.Equal(t, "developer", gjson.GetBytes(got, "messages.0.role").String())
	require.False(t, gjson.GetBytes(got, "tools").Exists())
	require.Equal(t, original, body)
	got, err = normalizeChatProviderRequest(account, "https://api.openai.com/v1/chat/completions", body)
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestChatProviderConstraints_PlanTokenLimitCoalesce(t *testing.T) {
	account := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL}}
	dual := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"max_tokens":128,"max_completion_tokens":256}`)
	original := bytes.Clone(dual)

	got, err := normalizeChatProviderRequest(account, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", dual)
	require.NoError(t, err)
	require.Equal(t, original, dual)
	require.EqualValues(t, 256, gjson.GetBytes(got, "max_tokens").Int())
	require.False(t, gjson.GetBytes(got, "max_completion_tokens").Exists())

	ali := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{"base_url": newapiintegration.AliTokenPlanBaseURL}}
	onlyCompletion := []byte(`{"model":"glm-5.3","max_completion_tokens":512,"messages":[{"role":"user","content":"hi"}]}`)
	got, err = normalizeChatProviderRequest(ali, newapiintegration.AliTokenPlanBaseURL+"/compatible-mode/v1/chat/completions", onlyCompletion)
	require.NoError(t, err)
	require.EqualValues(t, 512, gjson.GetBytes(got, "max_tokens").Int())
	require.False(t, gjson.GetBytes(got, "max_completion_tokens").Exists())

	onlyMax := []byte(`{"model":"glm-5.3","max_tokens":64}`)
	got, err = normalizeChatProviderRequest(account, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", onlyMax)
	require.NoError(t, err)
	require.Equal(t, onlyMax, got)

	for _, bad := range []string{
		`{"max_completion_tokens":null,"max_tokens":8}`,
		`{"max_completion_tokens":"256","max_tokens":8}`,
		`{"max_completion_tokens":-1,"max_tokens":8}`,
		`{"max_completion_tokens":0,"max_tokens":8}`,
	} {
		body := []byte(bad)
		got, err = normalizeChatProviderRequest(account, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", body)
		require.NoError(t, err)
		require.Equal(t, body, got)
	}

	foreign := []byte(`{"max_tokens":128,"max_completion_tokens":256}`)
	got, err = normalizeChatProviderRequest(account, "https://api.openai.com/v1/chat/completions", foreign)
	require.NoError(t, err)
	require.Equal(t, foreign, got)
	got, err = normalizeChatProviderRequest(account, "https://ark.cn-beijing.volces.com/api/v3/chat/completions", foreign)
	require.NoError(t, err)
	require.Equal(t, foreign, got)
}

func TestChatProviderConstraints_VolcPlanSamplingBounds(t *testing.T) {
	account := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL}}
	endpoint := newapiintegration.VolcEngineAgentPlanBaseURL + "/chat/completions"

	// Customer cn-prod evidence: temperature 2.5 and max_tokens 1000000 → Volc 400.
	over := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"temperature":2.5,"top_p":1.1,"max_tokens":1000000}`)
	original := bytes.Clone(over)
	got, err := normalizeChatProviderRequest(account, endpoint, over)
	require.NoError(t, err)
	require.Equal(t, original, over, "ingress for retry must stay untouched")
	require.InDelta(t, 2.0, gjson.GetBytes(got, "temperature").Float(), 1e-9)
	require.InDelta(t, 1.0, gjson.GetBytes(got, "top_p").Float(), 1e-9)
	require.EqualValues(t, volcPlanKimiK3MaxTokensMax, gjson.GetBytes(got, "max_tokens").Int())

	// Dual output limits: coalesce to max_tokens first, then clamp the budget.
	dual := []byte(`{"model":"kimi-k3","max_tokens":64,"max_completion_tokens":1000000,"temperature":99}`)
	got, err = normalizeChatProviderRequest(account, endpoint, dual)
	require.NoError(t, err)
	require.EqualValues(t, volcPlanKimiK3MaxTokensMax, gjson.GetBytes(got, "max_tokens").Int())
	require.False(t, gjson.GetBytes(got, "max_completion_tokens").Exists())
	require.InDelta(t, 2.0, gjson.GetBytes(got, "temperature").Float(), 1e-9)

	// Higher-output Volc Plan models keep sampling clamps but not the 128k token cap.
	deepseek := []byte(`{"model":"deepseek-v4-pro","temperature":2.5,"top_p":1.1,"max_tokens":200000}`)
	got, err = normalizeChatProviderRequest(account, endpoint, deepseek)
	require.NoError(t, err)
	require.InDelta(t, 2.0, gjson.GetBytes(got, "temperature").Float(), 1e-9)
	require.InDelta(t, 1.0, gjson.GetBytes(got, "top_p").Float(), 1e-9)
	require.EqualValues(t, 200000, gjson.GetBytes(got, "max_tokens").Int())

	// In-range values and missing fields stay unchanged.
	ok := []byte(`{"model":"kimi-k3","temperature":1.2,"top_p":0.9,"max_tokens":4096}`)
	got, err = normalizeChatProviderRequest(account, endpoint, ok)
	require.NoError(t, err)
	require.JSONEq(t, string(ok), string(got))

	// Negative sampling floors to zero; non-numeric values are left alone.
	neg := []byte(`{"temperature":-0.5,"top_p":-1,"max_tokens":"big"}`)
	got, err = normalizeChatProviderRequest(account, endpoint, neg)
	require.NoError(t, err)
	require.InDelta(t, 0.0, gjson.GetBytes(got, "temperature").Float(), 1e-9)
	require.InDelta(t, 0.0, gjson.GetBytes(got, "top_p").Float(), 1e-9)
	require.Equal(t, "big", gjson.GetBytes(got, "max_tokens").String())

	again, err := normalizeChatProviderRequest(account, endpoint, got)
	require.NoError(t, err)
	require.Equal(t, got, again, "clamp must be idempotent")

	// Ali Plan and Volc pay-as-you-go keep prior behavior (no Volc Plan clamp).
	ali := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{"base_url": newapiintegration.AliTokenPlanBaseURL}}
	aliBody := []byte(`{"temperature":2.5,"top_p":1.1,"max_tokens":1000000}`)
	got, err = normalizeChatProviderRequest(ali, newapiintegration.AliTokenPlanBaseURL+"/compatible-mode/v1/chat/completions", aliBody)
	require.NoError(t, err)
	require.JSONEq(t, string(aliBody), string(got))
	got, err = normalizeChatProviderRequest(account, "https://ark.cn-beijing.volces.com/api/v3/chat/completions", aliBody)
	require.NoError(t, err)
	require.JSONEq(t, string(aliBody), string(got))
}

// Emulates the observed provider defect: ignore tool_choice and call every
// advertised function. The gateway must constrain the actual outbound list.
func chatConstraintSupplier(body []byte) (string, string) {
	calls := make([]any, 0)
	for i, tool := range gjson.GetBytes(body, "tools").Array() {
		calls = append(calls, gin.H{"index": i, "id": fmt.Sprint("call_", i), "type": "function", "function": gin.H{"name": tool.Get("function.name").String(), "arguments": "{}"}})
	}
	message := gin.H{"role": "assistant", "content": "White"}
	finish := "stop"
	if len(calls) > 0 {
		message["content"] = nil
		message["tool_calls"] = calls
		finish = "tool_calls"
	}
	if gjson.GetBytes(body, "response_format.type").String() == "json_object" {
		message["content"] = `{"answer":"White","score":1}`
	}
	usage := gin.H{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	if gjson.GetBytes(body, "stream").Bool() {
		chunk, _ := json.Marshal(gin.H{"id": "test", "model": "glm-5.3", "choices": []any{gin.H{"index": 0, "delta": message, "finish_reason": finish}}})
		tail, _ := json.Marshal(gin.H{"id": "test", "model": "glm-5.3", "choices": []any{}, "usage": usage})
		return "text/event-stream", "data: " + string(chunk) + "\n\ndata: " + string(tail) + "\n\ndata: [DONE]\n\n"
	}
	out, _ := json.Marshal(gin.H{"id": "test", "model": "glm-5.3", "choices": []any{gin.H{"index": 0, "message": message, "finish_reason": finish}}, "usage": usage})
	return "application/json", string(out)
}

type chatConstraintTransport struct{ httpUpstreamRecorder }

func (u *chatConstraintTransport) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	contentType, response := chatConstraintSupplier(body)
	u.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}
	return u.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
}

func TestChatProviderConstraints_ForwardRawAndAliAdaptor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ali := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, universal := range []bool{false, true} {
				for _, mode := range []string{"named", "none", "json_object", "json_schema"} {
					t.Run(fmt.Sprintf("ali=%t/stream=%t/universal=%t/%s", ali, stream, universal, mode), func(t *testing.T) {
						request := gin.H{"model": "glm-5.3", "messages": []any{gin.H{"role": "developer", "content": "Return White"}, gin.H{"role": "user", "content": "weather and time"}}, "stream": stream, "max_tokens": 128, "max_completion_tokens": 256, "tools": json.RawMessage(constraintTools)}
						if mode == "named" {
							request["tool_choice"] = gin.H{"type": "function", "function": gin.H{"name": "get_weather"}}
						} else {
							request["tool_choice"] = "none"
						}
						if mode == "json_object" {
							request["response_format"] = gin.H{"type": "json_object"}
						}
						if mode == "json_schema" {
							request["response_format"] = json.RawMessage(`{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}}`)
						}
						body, err := json.Marshal(request)
						require.NoError(t, err)
						original := bytes.Clone(body)
						account := &Account{ID: 42, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeVolcEngine,
							Credentials: map[string]any{"api_key": "test-key", "base_url": newapiintegration.VolcEngineAgentPlanBaseURL}}
						account.Credentials["api_protocol"] = APIProtocolChatCompletions
						account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
						transport := &chatConstraintTransport{}
						svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: transport}
						var wire []byte
						if ali {
							account.ChannelType = newapiconstant.ChannelTypeAli
							account.Credentials["base_url"] = newapiintegration.AliTokenPlanBaseURL
							server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								wire, _ = io.ReadAll(r.Body)
								if r.URL.Path != "/compatible-mode/v1/chat/completions" {
									http.Error(w, "wrong path", 404)
									return
								}
								for _, msg := range gjson.GetBytes(wire, "messages").Array() {
									if msg.Get("role").String() == "developer" {
										http.Error(w, "unsupported role", 400)
										return
									}
								}
								contentType, response := chatConstraintSupplier(wire)
								w.Header().Set("Content-Type", contentType)
								fmt.Fprint(w, response)
							}))
							defer server.Close()
							previous := dispatchNewAPIChatCompletions
							t.Cleanup(func() { dispatchNewAPIChatCompletions = previous })
							dispatchNewAPIChatCompletions = func(ctx context.Context, c *gin.Context, in bridge.ChannelContextInput, b []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
								require.Equal(t, newapiintegration.AliTokenPlanBaseURL, in.BaseURL)
								in.BaseURL = server.URL // Only redirect transport; execute the real Ali adaptor.
								return bridge.DispatchChatCompletions(ctx, c, in, b)
							}
						}
						ctx := context.Background()
						if universal {
							ctx = WithUniversalKeyRouting(ctx)
						}
						rec := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(rec)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
						c.Request.Header.Set("Content-Type", "application/json")
						result, err := svc.ForwardAsChatCompletionsDispatched(ctx, c, account, body, "", "")
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						require.Equal(t, 10, result.Usage.InputTokens)
						if !ali {
							wire = transport.lastBody
						}
						role := "developer"
						if ali {
							role = "system"
						}
						require.Equal(t, role, gjson.GetBytes(wire, "messages.0.role").String())
						require.Equal(t, "Return White", gjson.GetBytes(wire, "messages.0.content").String())
						require.EqualValues(t, 256, gjson.GetBytes(wire, "max_tokens").Int())
						require.False(t, gjson.GetBytes(wire, "max_completion_tokens").Exists())
						if mode == "named" {
							require.Equal(t, int64(1), gjson.GetBytes(wire, "tools.#").Int())
							require.Contains(t, rec.Body.String(), "get_weather")
							require.NotContains(t, rec.Body.String(), "get_time")
						} else {
							require.Empty(t, gjson.GetBytes(wire, "tools").Array())
							require.NotContains(t, rec.Body.String(), "tool_calls")
						}
						if strings.HasPrefix(mode, "json_") {
							require.JSONEq(t, gjson.GetBytes(body, "response_format").Raw, gjson.GetBytes(wire, "response_format").Raw)
						}
						if mode == "json_object" {
							content := gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String()
							if stream {
								for _, line := range strings.Split(rec.Body.String(), "\n") {
									data, _ := extractOpenAISSEDataLine(line)
									content += gjson.Get(data, "choices.0.delta.content").String()
								}
							}
							require.JSONEq(t, `{"answer":"White","score":1}`, content)
						}
						if stream {
							require.Contains(t, rec.Body.String(), "data: [DONE]")
						}
						require.Equal(t, original, body)
					})
				}
			}
		}
	}
}

func TestChatProviderConstraints_InvalidSelectionDoesNotSend(t *testing.T) {
	for _, ali := range []bool{true, false} {
		t.Run(fmt.Sprint("ali=", ali), func(t *testing.T) {
			account := &Account{ID: 42, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeVolcEngine,
				Credentials: map[string]any{"api_key": "test-key", "base_url": newapiintegration.VolcEngineAgentPlanBaseURL, "api_protocol": APIProtocolChatCompletions},
				Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: false}}
			if ali {
				account.ChannelType = newapiconstant.ChannelTypeAli
				account.Credentials["base_url"] = newapiintegration.AliTokenPlanBaseURL
			}
			previous := dispatchNewAPIChatCompletions
			t.Cleanup(func() { dispatchNewAPIChatCompletions = previous })
			dispatchNewAPIChatCompletions = func(context.Context, *gin.Context, bridge.ChannelContextInput, []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
				t.Fatal("invalid choice reached the bridge transport")
				return nil, nil
			}
			transport := &chatConstraintTransport{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: transport}
			body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"secret-prompt"}],"tools":` + constraintTools + `,"tool_choice":{"type":"function","function":{"name":"secret-missing"}}}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			_, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.NotContains(t, rec.Body.String(), "secret")
			require.Empty(t, transport.requests)
		})
	}
}

func TestChatProviderConstraints_ResponsesFallback(t *testing.T) {
	for _, ali := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, choice := range []string{`"none"`, `{"type":"function","name":"get_weather"}`} {
				t.Run(fmt.Sprintf("ali=%t/stream=%t/%s", ali, stream, choice), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":"glm-5.3","input":[{"role":"developer","content":"Return White"},{"role":"user","content":"weather and time"}],"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}},{"type":"function","name":"get_time","parameters":{"type":"object"}}],"tool_choice":%s,"stream":%t}`, choice, stream))
					original := bytes.Clone(body)
					account := &Account{ID: 42, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeVolcEngine,
						Credentials: map[string]any{"api_key": "test-key", "base_url": newapiintegration.VolcEngineAgentPlanBaseURL}}
					transport := &chatConstraintTransport{}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: transport}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					var result *OpenAIForwardResult
					var err error
					var wire []byte
					if ali {
						account.ChannelType = newapiconstant.ChannelTypeAli
						account.Credentials["base_url"] = newapiintegration.AliTokenPlanBaseURL
						previous := dispatchNewAPIChatCompletions
						t.Cleanup(func() { dispatchNewAPIChatCompletions = previous })
						dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
							wire = bytes.Clone(body)
							contentType, response := chatConstraintSupplier(body)
							c.Header("Content-Type", contentType)
							_, _ = c.Writer.WriteString(response)
							return &bridge.DispatchOutcome{Model: "glm-5.3", UpstreamModel: "glm-5.3", Stream: stream, Usage: &newapidto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}, nil
						}
						result, err = svc.forwardResponsesViaNewAPIBridgeChatCompletions(context.Background(), c, account, body, newAPIBridgeChannelInputForBody(account, 0, "", body))
					} else {
						result, err = svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, body)
						wire = transport.lastBody
					}
					require.NoError(t, err)
					require.Equal(t, 10, result.Usage.InputTokens)
					if choice == `"none"` {
						require.Empty(t, gjson.GetBytes(wire, "tools").Array())
						require.NotContains(t, rec.Body.String(), "function_call")
					} else {
						require.Equal(t, int64(1), gjson.GetBytes(wire, "tools.#").Int())
						require.Contains(t, rec.Body.String(), "get_weather")
						require.NotContains(t, rec.Body.String(), "get_time")
					}
					require.Equal(t, original, body)
					require.Equal(t, "/v1/responses", c.Request.URL.Path)
				})
			}
		}
	}
}
