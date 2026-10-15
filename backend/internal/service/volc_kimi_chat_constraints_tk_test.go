//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestVolcKimiThinkingSwitchesAndSupplierScope(t *testing.T) {
	for _, field := range []string{
		`"thinking":{"type":"disabled"}`, `"thinking":false`, `"enable_thinking":false`,
		`"chat_template_kwargs":{"enable_thinking":false,"preserve":9007199254740993}`,
		`"chat_template_kwargs":{"thinking":false}`, `"chat_template_kwargs":{"thinking":{"type":"disabled"}}`,
	} {
		t.Run(field, func(t *testing.T) {
			body := []byte(`{"model":"kimi-k3",` + field + `,"stream":true,"reasoning_effort":"high","metadata":{"n":9007199254740993}}`)
			original := bytes.Clone(body)
			got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", body)
			require.NoError(t, err)
			require.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String())
			require.False(t, gjson.GetBytes(got, "enable_thinking").Exists())
			require.False(t, gjson.GetBytes(got, "chat_template_kwargs.thinking").Exists())
			require.False(t, gjson.GetBytes(got, "chat_template_kwargs.enable_thinking").Exists())
			require.Equal(t, "high", gjson.GetBytes(got, "reasoning_effort").String())
			require.Equal(t, "9007199254740993", gjson.GetBytes(got, "metadata.n").Raw)
			require.Equal(t, gjson.GetBytes(body, "chat_template_kwargs.preserve").Raw, gjson.GetBytes(got, "chat_template_kwargs.preserve").Raw)
			require.Equal(t, original, body)
			again, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", got)
			require.NoError(t, err)
			require.JSONEq(t, string(got), string(again))
		})
	}
	for _, body := range []string{`{"thinking":{"type":"enabled"}}`, `{"reasoning_effort":"low"}`, `{}`, `{"tool_choice":"auto"}`, `{"tool_choice":"required"}`, `{"tool_choice":"none"}`} {
		got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(body))
		require.NoError(t, err)
		require.JSONEq(t, body, string(got))
	}
	body := []byte(`{"thinking":{"type":"disabled"}}`)
	other, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(other, "thinking").Exists())
	require.Equal(t, "low", gjson.GetBytes(other, "reasoning_effort").String())
	untouched, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "glm-5.3", body)
	require.NoError(t, err)
	require.Equal(t, body, untouched)
	for _, bad := range []string{`{"thinking":false,"enable_thinking":true}`, `{"thinking":"disabled"}`, `{"enable_thinking":"false"}`, `{"thinking":{"type":"wrong"}}`} {
		_, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(bad))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
	}
}

func TestVolcKimiToolSelectionPreservesSemantics(t *testing.T) {
	for _, mode := range []string{"auto", "required"} {
		for _, ref := range []string{`{"type":"function","function":{"name":"get_weather"}}`, `{"type":"function","name":"get_weather"}`} {
			body := []byte(`{"tools":` + constraintTools + `,"tool_choice":{"type":"allowed_tools","mode":"` + mode + `","tools":[` + ref + `]},"messages":[{"role":"tool","tool_call_id":"old","content":"history"}],"metadata":{"n":9007199254740993}}`)
			got, err := normalizeVolcKimiChatRequest(body)
			require.NoError(t, err)
			require.Equal(t, mode, gjson.GetBytes(got, "tool_choice").String())
			require.EqualValues(t, 1, gjson.GetBytes(got, "tools.#").Int())
			require.JSONEq(t, gjson.Get(constraintTools, "0").Raw, gjson.GetBytes(got, "tools.0").Raw)
			require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(got, "messages").Raw)
			require.Equal(t, "9007199254740993", gjson.GetBytes(got, "metadata.n").Raw)
		}
	}
	body := []byte(`{"tools":` + constraintTools + `,"tool_choice":{"type":"function","function":{"name":"get_weather"}},"thinking":{"type":"enabled"}}`)
	got, err := normalizeVolcKimiChatRequest(body)
	require.NoError(t, err)
	require.Equal(t, "required", gjson.GetBytes(got, "tool_choice").String())
	require.EqualValues(t, 1, gjson.GetBytes(got, "tools.#").Int())
	require.Equal(t, "get_weather", gjson.GetBytes(got, "tools.0.function.name").String())
	require.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String())
	for _, selection := range []string{
		`{"type":"allowed_tools","mode":"required","tools":[]}`,
		`{"type":"allowed_tools","mode":"none","tools":[{"type":"function","name":"get_weather"}]}`,
		`{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"missing"}]}`,
		`{"type":"allowed_tools","mode":"auto","tools":[{"type":"web_search"}]}`,
		`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"get_weather"},{"type":"function","name":"get_weather"}]}`,
		`{"type":"function","function":{"name":"missing"}}`,
	} {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"tools":` + constraintTools + `,"tool_choice":` + selection + `}`))
		require.Error(t, err)
	}
	allowed := `"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"get_weather"}]}`
	for _, extra := range []string{`"functions":[]`, `"function_call":"auto"`} {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"tools":` + constraintTools + `,` + allowed + `,` + extra + `}`))
		require.Error(t, err)
	}
	_, err = normalizeVolcKimiChatRequest([]byte(`{"tools":[` + gjson.Get(constraintTools, "0").Raw + `,` + gjson.Get(constraintTools, "0").Raw + `],` + allowed + `}`))
	require.Error(t, err)
}

func TestVolcKimiChatAllowedToolsEnvelope(t *testing.T) {
	for _, mode := range []string{"auto", "required"} {
		// Chat SDKs nest mode/tools under allowed_tools. Reverse reference order
		// must not reorder or lose the original full function definitions.
		choice := `{"type":"allowed_tools","allowed_tools":{"mode":"` + mode + `","tools":[{"type":"function","function":{"name":"get_time"}},{"type":"function","function":{"name":"get_weather"}}]}}`
		body := []byte(`{"tools":` + constraintTools + `,"tool_choice":` + choice + `}`)
		original := bytes.Clone(body)
		got, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.Equal(t, mode, gjson.GetBytes(got, "tool_choice").String())
		require.JSONEq(t, constraintTools, gjson.GetBytes(got, "tools").Raw)
		require.Equal(t, original, body)
	}
	for _, choice := range []string{
		`{"type":"allowed_tools","allowed_tools":null}`,
		`{"type":"allowed_tools","allowed_tools":[]}`,
		`{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[]}}`,
		`{"type":"allowed_tools","mode":"auto","allowed_tools":{"mode":"required","tools":[{"type":"function","name":"get_weather"}]}}`,
		`{"type":"allowed_tools","tools":[],"allowed_tools":{"mode":"required","tools":[{"type":"function","name":"get_weather"}]}}`,
	} {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"tools":` + constraintTools + `,"tool_choice":` + choice + `}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
	}
}

// Exercise the actual raw gateway forwarder and bridge boundary, not just the
// shaping helper: the selected supplier receives the off switch and tool subset.
func TestVolcKimiForwardKeepsThinkingAndToolConstraints(t *testing.T) {
	for _, route := range []string{"raw", "admin", "dispatch"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("route=%s/stream=%v", route, stream), func(t *testing.T) {
				account := volcEnginePlanTestAccount()
				account.ID = 42
				account.Credentials["api_key"] = "test-key"
				account.Credentials["api_protocol"] = APIProtocolChatCompletions
				account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
				body := []byte(fmt.Sprintf(`{"model":"kimi-k3","stream":%v,"messages":[{"role":"user","content":"weather"}],"enable_thinking":false,"tools":%s,"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"get_weather"}}]}}}`, stream, constraintTools))
				transport := &chatConstraintTransport{}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: transport}
				var wire []byte
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				if route == "admin" {
					previous := dispatchNewAPIChatCompletions
					t.Cleanup(func() { dispatchNewAPIChatCompletions = previous })
					dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, _ bridge.ChannelContextInput, b []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
						wire = bytes.Clone(b)
						ct, response := chatConstraintSupplier(b)
						c.Data(http.StatusOK, ct, []byte(response))
						return &bridge.DispatchOutcome{Model: "kimi-k3", Stream: stream}, nil
					}
					err := dispatchNewAPIAccountTestChatCompletions(context.Background(), c, account, body)
					require.NoError(t, err)
				} else {
					var err error
					if route == "dispatch" {
						// Production dispatch deliberately bypasses the PAYG adaptor
						// for Agent Plan. Capture its actual raw transport boundary.
						_, err = svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
					} else {
						_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					}
					require.NoError(t, err)
					wire = transport.lastBody
				}
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, "disabled", gjson.GetBytes(wire, "thinking.type").String())
				require.Equal(t, "required", gjson.GetBytes(wire, "tool_choice").String())
				require.EqualValues(t, 1, gjson.GetBytes(wire, "tools.#").Int())
				require.Contains(t, rec.Body.String(), "get_weather")
				require.NotContains(t, rec.Body.String(), "get_time")
			})
		}
	}
}
