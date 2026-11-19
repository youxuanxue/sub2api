//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
			require.False(t, gjson.GetBytes(got, "reasoning_effort").Exists(), "disabled must strip effort before relay")
			require.Equal(t, "9007199254740993", gjson.GetBytes(got, "metadata.n").Raw)
			require.Equal(t, gjson.GetBytes(body, "chat_template_kwargs.preserve").Raw, gjson.GetBytes(got, "chat_template_kwargs.preserve").Raw)
			require.Equal(t, original, body)
			again, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", got)
			require.NoError(t, err)
			require.JSONEq(t, string(got), string(again))
		})
	}
	for _, body := range []string{`{"thinking":{"type":"enabled"}}`, `{}`, `{"tool_choice":"auto"}`, `{"tool_choice":"required"}`, `{"tool_choice":"none"}`} {
		got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(body))
		require.NoError(t, err)
		require.JSONEq(t, body, string(got))
	}
	effortOnly, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(`{"reasoning_effort":"low"}`))
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(effortOnly, "reasoning_effort").String())
	require.Equal(t, "enabled", gjson.GetBytes(effortOnly, "thinking.type").String())
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
	blank, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(`{"thinking":{"type":""},"reasoning_effort":"max","messages":[{"role":"user","content":"ping"}]}`))
	require.NoError(t, err)
	require.Equal(t, "enabled", gjson.GetBytes(blank, "thinking.type").String())
	require.Equal(t, "max", gjson.GetBytes(blank, "reasoning_effort").String())
	whitespace, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(`{"thinking":{"type":"  "},"reasoning_effort":"xhigh"}`))
	require.NoError(t, err)
	require.Equal(t, "enabled", gjson.GetBytes(whitespace, "thinking.type").String())
	require.Equal(t, "max", gjson.GetBytes(whitespace, "reasoning_effort").String())
	blankOnly, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(`{"thinking":{"type":""}}`))
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(blankOnly, "thinking.type").String())
	disabledWins, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(`{"thinking":{"type":""},"enable_thinking":false,"reasoning_effort":"high"}`))
	require.NoError(t, err)
	require.Equal(t, "disabled", gjson.GetBytes(disabledWins, "thinking.type").String())
	require.False(t, gjson.GetBytes(disabledWins, "reasoning_effort").Exists())
}

func TestVolcKimiToolSelectionPreservesSemantics(t *testing.T) {
	for _, mode := range []string{"auto", "required"} {
		for _, ref := range []string{`{"type":"function","function":{"name":"get_weather"}}`, `{"type":"function","name":"get_weather"}`} {
			body := []byte(`{"tools":` + constraintTools + `,"tool_choice":{"type":"allowed_tools","mode":"` + mode + `","tools":[` + ref + `]},"messages":[{"role":"assistant","tool_calls":[{"id":"old","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"old","content":"history"}],"metadata":{"n":9007199254740993}}`)
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

func TestVolcKimiReasoningEffortMoonshotCompatible(t *testing.T) {
	t.Run("aliases_and_enable", func(t *testing.T) {
		for _, tc := range []struct {
			in, wantEffort string
		}{
			{`{"reasoning_effort":"highest"}`, "max"},
			{`{"reasoning_effort":"medium"}`, "high"},
			{`{"reasoning":{"effort":"xhigh"}}`, "max"},
			{`{"reasoning_effort":"low","thinking":{"type":"enabled","budget_tokens":1024}}`, "low"},
		} {
			got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", []byte(tc.in))
			require.NoError(t, err, tc.in)
			require.Equal(t, tc.wantEffort, gjson.GetBytes(got, "reasoning_effort").String(), tc.in)
			require.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String(), tc.in)
			require.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists(), tc.in)
			require.False(t, gjson.GetBytes(got, "reasoning.effort").Exists(), tc.in)
		}
	})
	t.Run("disabled_wins_over_effort", func(t *testing.T) {
		got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3",
			[]byte(`{"thinking":{"type":"disabled"},"reasoning_effort":"highest"}`))
		require.NoError(t, err)
		require.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String())
		require.False(t, gjson.GetBytes(got, "reasoning_effort").Exists())
	})
	t.Run("invalid_effort_400", func(t *testing.T) {
		_, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3",
			[]byte(`{"reasoning_effort":"invalid"}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "reasoning_effort", invalid.Param)
	})
	t.Run("thinking_effort_wins", func(t *testing.T) {
		got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3",
			[]byte(`{"thinking":{"type":"enabled","keep":"all","effort":"low"},"reasoning_effort":"max"}`))
		require.NoError(t, err)
		require.Equal(t, "low", gjson.GetBytes(got, "reasoning_effort").String())
		require.Equal(t, "low", gjson.GetBytes(got, "thinking.effort").String())
		require.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String())
		require.Equal(t, "all", gjson.GetBytes(got, "thinking.keep").String())
	})
	t.Run("thinking_effort_alone_sets_reasoning_effort", func(t *testing.T) {
		got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3",
			[]byte(`{"thinking":{"type":"enabled","effort":"highest"}}`))
		require.NoError(t, err)
		require.Equal(t, "max", gjson.GetBytes(got, "reasoning_effort").String())
		require.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String())
	})
	t.Run("disabled_ignores_thinking_effort", func(t *testing.T) {
		got, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3",
			[]byte(`{"thinking":{"type":"disabled","effort":"low"},"reasoning_effort":"max"}`))
		require.NoError(t, err)
		require.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String())
		require.False(t, gjson.GetBytes(got, "reasoning_effort").Exists())
	})
	t.Run("invalid_thinking_effort_400", func(t *testing.T) {
		_, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3",
			[]byte(`{"thinking":{"type":"enabled","effort":"banana"},"reasoning_effort":"max"}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "thinking.effort", invalid.Param)
	})
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

func TestVolcKimiInboundMessageShapes(t *testing.T) {
	account := volcEnginePlanTestAccount()

	object, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", []byte(`{"model":"kimi-k3","messages":[{"role":"system","content":"ok"},{"role":"user","content":{"key":"val"}}]}`))
	require.NoError(t, err)
	require.Equal(t, "ok", gjson.GetBytes(object, "messages.0.content").String())
	require.Equal(t, `{"key":"val"}`, gjson.GetBytes(object, "messages.1.content").String())
	require.False(t, gjson.GetBytes(object, "thinking").Exists())

	part, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":{"type":"text","text":"hi"}}]}`))
	require.NoError(t, err)
	require.Equal(t, "text", gjson.GetBytes(part, "messages.0.content.0.type").String())
	require.Equal(t, "hi", gjson.GetBytes(part, "messages.0.content.0.text").String())

	array, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/a.webm"}},{"type":"text","text":"hi"}]}]}`))
	require.NoError(t, err)
	require.Equal(t, "video_url", gjson.GetBytes(array, "messages.0.content.0.type").String())
	require.Equal(t, "https://example.com/a.webm", gjson.GetBytes(array, "messages.0.content.0.video_url.url").String())

	stringVideo, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"video_url","video_url":"https://example.com/a.mp4"}]}]}`))
	require.NoError(t, err)
	require.Equal(t, "https://example.com/a.mp4", gjson.GetBytes(stringVideo, "messages.0.content.1.video_url.url").String())
	require.Equal(t, "hi", gjson.GetBytes(stringVideo, "messages.0.content.0.text").String())

	singleStringVideo, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":{"type":"video_url","video_url":"ms://file-1"}}]}`))
	require.NoError(t, err)
	require.Equal(t, "ms://file-1", gjson.GetBytes(singleStringVideo, "messages.0.content.0.video_url.url").String())

	for _, orphanBody := range []string{
		`{"messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`,
		`{"messages":[{"role":"tool","content":"some tool result"},{"role":"user","content":"hello"}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"  ","content":"result"}]}`,
	} {
		_, err := normalizeVolcKimiChatRequest([]byte(orphanBody))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "messages", invalid.Param)
		require.Equal(t, "invalid_parameter", invalid.Code)
		require.NotContains(t, strings.ToLower(invalid.Message), "volc")
	}

	matched, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`))
	require.NoError(t, err)
	require.Equal(t, "tool", gjson.GetBytes(matched, "messages.1.role").String())
	require.Equal(t, "call_1", gjson.GetBytes(matched, "messages.1.tool_call_id").String())

	// cn-prod 2026-11-17: Claude-Code-style trajectories insert assistant text
	// between tool_calls and tool results; Volc 400s until those turns are dropped.
	interleaved := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"assistant","content":null,"tool_calls":[{"id":"Bash_0_a","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"assistant","content":"progress"},{"role":"tool","tool_call_id":"Bash_0_a","content":"ok"},{"role":"assistant","content":null,"tool_calls":[{"id":"Bash_0_b","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"assistant","content":"more"},{"role":"tool","tool_call_id":"Bash_0_b","content":"ok2"}]}`)
	fixed, err := normalizeVolcKimiChatRequest(interleaved)
	require.NoError(t, err)
	require.EqualValues(t, 6, gjson.GetBytes(fixed, "messages.#").Int())
	require.Equal(t, "assistant", gjson.GetBytes(fixed, "messages.2.role").String())
	require.Equal(t, "Bash_0_a", gjson.GetBytes(fixed, "messages.2.tool_calls.0.id").String())
	require.Equal(t, "tool", gjson.GetBytes(fixed, "messages.3.role").String())
	require.Equal(t, "Bash_0_a", gjson.GetBytes(fixed, "messages.3.tool_call_id").String())
	require.Equal(t, "assistant", gjson.GetBytes(fixed, "messages.4.role").String())
	require.Equal(t, "Bash_0_b", gjson.GetBytes(fixed, "messages.4.tool_calls.0.id").String())
	require.Equal(t, "tool", gjson.GetBytes(fixed, "messages.5.role").String())

	// Consecutive interleaved assistant texts in one tool round-trip are all dropped.
	multiInterleaved := []byte(`{"messages":[{"role":"user","content":"u"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"assistant","content":"a"},{"role":"assistant","content":"b"},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	multiFixed, err := normalizeVolcKimiChatRequest(multiInterleaved)
	require.NoError(t, err)
	require.EqualValues(t, 3, gjson.GetBytes(multiFixed, "messages.#").Int())
	require.Equal(t, "assistant", gjson.GetBytes(multiFixed, "messages.1.role").String())
	require.Equal(t, "c1", gjson.GetBytes(multiFixed, "messages.1.tool_calls.0.id").String())
	require.Equal(t, "tool", gjson.GetBytes(multiFixed, "messages.2.role").String())
	require.Equal(t, "c1", gjson.GetBytes(multiFixed, "messages.2.tool_call_id").String())

	// Parallel tool_calls: assistant text between tool results is also dropped.
	parallelInterleaved := []byte(`{"messages":[{"role":"user","content":"u"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"A","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"B","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"ra"},{"role":"assistant","content":"progress"},{"role":"tool","tool_call_id":"b","content":"rb"}]}`)
	parallelFixed, err := normalizeVolcKimiChatRequest(parallelInterleaved)
	require.NoError(t, err)
	require.EqualValues(t, 4, gjson.GetBytes(parallelFixed, "messages.#").Int())
	require.Equal(t, "tool", gjson.GetBytes(parallelFixed, "messages.2.role").String())
	require.Equal(t, "a", gjson.GetBytes(parallelFixed, "messages.2.tool_call_id").String())
	require.Equal(t, "tool", gjson.GetBytes(parallelFixed, "messages.3.role").String())
	require.Equal(t, "b", gjson.GetBytes(parallelFixed, "messages.3.tool_call_id").String())

	// Ordinary assistant text outside a tool round-trip must stay.
	plain := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"next"}]}`)
	keptPlain, err := normalizeVolcKimiChatRequest(plain)
	require.NoError(t, err)
	require.EqualValues(t, 3, gjson.GetBytes(keptPlain, "messages.#").Int())
	require.Equal(t, "hello", gjson.GetBytes(keptPlain, "messages.1.content").String())

	for _, emptyBody := range []string{
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"","parameters":{"type":"object"}}},{"type":"function","function":{"name":"  ","parameters":{"type":"object"}}}],"tool_choice":"required"}`,
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"","parameters":{"type":"object"}}},{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`,
	} {
		_, err := normalizeVolcKimiChatRequest([]byte(emptyBody))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "tools", invalid.Param)
		require.Equal(t, "invalid_parameter", invalid.Code)
		require.NotContains(t, strings.ToLower(invalid.Message), "volc")
	}

	kept, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`))
	require.NoError(t, err)
	require.EqualValues(t, 1, gjson.GetBytes(kept, "tools.#").Int())
	require.Equal(t, "lookup", gjson.GetBytes(kept, "tools.0.function.name").String())
	require.Equal(t, "auto", gjson.GetBytes(kept, "tool_choice").String())

	// KVV empty name lives on a system message. Promotion runs first and the
	// blank name must still be visible on top-level tools.
	promotedEmpty, err := applyMoonshotK3DynamicToolsShape("kimi-k3", []byte(`{"model":"kimi-k3","messages":[{"role":"system","content":"","tools":[{"type":"function","function":{"name":""}}]},{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(promotedEmpty, "tools.0.function.name").String())
	_, err = applyMoonshotThinkingShape(nil, account, "kimi-k3", promotedEmpty)
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "tools", invalid.Param)

	promotedNamed, err := applyMoonshotK3DynamicToolsShape("kimi-k3", []byte(`{"model":"kimi-k3","messages":[{"role":"system","content":"","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]},{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	named, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", promotedNamed)
	require.NoError(t, err)
	require.Equal(t, "lookup", gjson.GetBytes(named, "tools.0.function.name").String())

	other, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", []byte(`{"messages":[{"role":"user","content":{"key":"val"}}]}`))
	require.NoError(t, err)
	require.Equal(t, "val", gjson.GetBytes(other, "messages.0.content.key").String())
}

func stubVolcKimiVideoProbe(t *testing.T, fn func(context.Context, string) error) {
	t.Helper()
	previous := probeVolcKimiVideoURL
	probeVolcKimiVideoURL = fn
	t.Cleanup(func() { probeVolcKimiVideoURL = previous })
}

func TestVolcKimiVideoURLPreflight(t *testing.T) {
	account := volcEnginePlanTestAccount()
	videoBody := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://cdn.example/a.webm"}},{"type":"text","text":"hi"}]}]}`)

	t.Run("unreachable", func(t *testing.T) {
		stubVolcKimiVideoProbe(t, func(context.Context, string) error {
			return fmt.Errorf("dial")
		})
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		got, err := applyMoonshotThinkingShape(c, account, "kimi-k3", videoBody)
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "messages.content", invalid.Param)
		require.Equal(t, volcKimiVideoUnreachableMessage, invalid.Message)
		require.NotContains(t, invalid.Message, "cdn.example")
		require.JSONEq(t, string(videoBody), string(got))
		require.Equal(t, "video_url", gjson.GetBytes(got, "messages.0.content.0.type").String())
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), volcKimiVideoUnreachableMessage)
		require.Contains(t, rec.Body.String(), `"param":"messages.content"`)
	})

	t.Run("reachable stays on the wire", func(t *testing.T) {
		stubVolcKimiVideoProbe(t, func(context.Context, string) error { return nil })
		got, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", videoBody)
		require.NoError(t, err)
		require.Equal(t, "https://cdn.example/a.webm", gjson.GetBytes(got, "messages.0.content.0.video_url.url").String())
		require.Equal(t, "hi", gjson.GetBytes(got, "messages.0.content.1.text").String())
	})

	t.Run("text and non-volc do not probe", func(t *testing.T) {
		called := 0
		stubVolcKimiVideoProbe(t, func(context.Context, string) error {
			called++
			return fmt.Errorf("should not probe")
		})
		text, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`))
		require.NoError(t, err)
		require.Equal(t, "hi", gjson.GetBytes(text, "messages.0.content").String())
		other, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", videoBody)
		require.NoError(t, err)
		require.Equal(t, "https://cdn.example/a.webm", gjson.GetBytes(other, "messages.0.content.0.video_url.url").String())
		require.Zero(t, called)
	})

	t.Run("inline and supplier schemes skip the fetch", func(t *testing.T) {
		largeData := "data:video/mp4;base64," + strings.Repeat("A", ((volcKimiMinDataVideoBytes+2)/3)*4)
		for _, rawURL := range []string{largeData, "ms://video-1"} {
			body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"` + rawURL + `"}}]}]}`)
			got, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", body)
			require.NoError(t, err)
			require.Equal(t, rawURL, gjson.GetBytes(got, "messages.0.content.0.video_url.url").String())
		}
		for _, rawURL := range []string{
			"",
			"data:video/mp4;base64,",
			"data:video/mp4;base64,AAAA",
			"data:text/plain;base64," + strings.Repeat("A", ((volcKimiMinDataVideoBytes+2)/3)*4),
			"data:video/mp4," + strings.Repeat("A", volcKimiMinDataVideoBytes),
			"http://127.0.0.1/secret.mp4",
			"http://169.254.169.254/latest/meta-data",
		} {
			body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"` + rawURL + `"}}]}]}`)
			_, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", body)
			var invalid *OpenAIInvalidParameterError
			require.ErrorAs(t, err, &invalid)
			require.Equal(t, volcKimiVideoUnreachableMessage, invalid.Message)
			require.NotContains(t, invalid.Error(), "127.0.0.1")
			require.NotContains(t, invalid.Error(), "169.254.169.254")
		}
	})
}

func TestVolcKimiHTTPVideoProbe(t *testing.T) {
	methods := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods[r.URL.Path] = append(methods[r.URL.Path], r.Method)
		switch r.URL.Path {
		case "/ok.mp4":
			w.WriteHeader(http.StatusOK)
		case "/no-head.mp4":
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.Header.Get("Range") != "bytes=0-0" {
				http.Error(w, "range", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusPartialContent)
		case "/missing.mp4":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	client := srv.Client()

	require.NoError(t, probeVolcKimiHTTPVideo(ctx, client, srv.URL+"/ok.mp4"))
	require.Equal(t, []string{http.MethodHead}, methods["/ok.mp4"])

	require.NoError(t, probeVolcKimiHTTPVideo(ctx, client, srv.URL+"/no-head.mp4"))
	require.Equal(t, []string{http.MethodHead, http.MethodGet}, methods["/no-head.mp4"])

	require.Error(t, probeVolcKimiHTTPVideo(ctx, client, srv.URL+"/missing.mp4"))
	require.Equal(t, []string{http.MethodHead}, methods["/missing.mp4"])

	srv.Close()
	require.Error(t, probeVolcKimiHTTPVideo(ctx, client, srv.URL+"/ok.mp4"))
}

// cn-prod 2026-11-06: Volc returned an unnamed InvalidParameter for any
// {"type":"text","text":""} part (system, user, assistant, tool). Direct
// controls showed whitespace text and content "" are accepted.
func TestVolcKimiDropsEmptyTextParts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		path string
		want string
	}{
		{"system empty first", `{"messages":[{"role":"system","content":[{"type":"text","text":""},{"type":"text","text":"rules"}]},{"role":"user","content":"hi"}]}`, "messages.0.content", `[{"type":"text","text":"rules"}]`},
		{"system empty last", `{"messages":[{"role":"system","content":[{"type":"text","text":"rules"},{"type":"text","text":""}]}]}`, "messages.0.content", `[{"type":"text","text":"rules"}]`},
		{"only empty part", `{"messages":[{"role":"system","content":[{"type":"text","text":""}]},{"role":"user","content":"hi"}]}`, "messages.0.content", `""`},
		{"user keeps image", `{"messages":[{"role":"user","content":[{"type":"text","text":""},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`, "messages.0.content", `[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]`},
		{"tool result", `{"messages":[{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":""},{"type":"text","text":"r"}]}]}`, "messages.1.content", `[{"type":"text","text":"r"}]`},
		{"whitespace kept", `{"messages":[{"role":"user","content":[{"type":"text","text":" "},{"type":"text","text":"hi"}]}]}`, "messages.0.content", `[{"type":"text","text":" "},{"type":"text","text":"hi"}]`},
		{"string content untouched", `{"messages":[{"role":"user","content":""}]}`, "messages.0.content", `""`},
		{"single object part wrapped then dropped", `{"messages":[{"role":"system","content":{"type":"text","text":""}},{"role":"user","content":"hi"}]}`, "messages.0.content", `""`},
		// cn-prod 2026-11-17 tokensea_online: type=text with no text field → Volc
		// "missing messages.content.text"; drop like empty text.
		{"missing text field", `{"messages":[{"role":"user","content":[{"type":"text"},{"type":"text","text":"hi"}]}]}`, "messages.0.content", `[{"type":"text","text":"hi"}]`},
		{"null text field", `{"messages":[{"role":"user","content":[{"type":"text","text":null},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`, "messages.0.content", `[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]`},
		{"only missing text part", `{"messages":[{"role":"user","content":[{"type":"text"}]},{"role":"user","content":"hi"}]}`, "messages.0.content", `""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := normalizeVolcKimiChatRequest([]byte(tc.in))
			require.NoError(t, err)
			require.JSONEq(t, tc.want, gjson.GetBytes(out, tc.path).Raw)
		})
	}

	t.Run("null message content becomes empty string", func(t *testing.T) {
		// cn-prod 2026-11-18 rt@tk.com wire: system content null → Volc missing messages.content.
		in := []byte(`{"messages":[{"role":"system","content":null},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`)
		out, err := normalizeVolcKimiChatRequest(in)
		require.NoError(t, err)
		require.Equal(t, gjson.String, gjson.GetBytes(out, "messages.0.content").Type)
		require.Equal(t, "", gjson.GetBytes(out, "messages.0.content").String())
		require.Equal(t, "hi", gjson.GetBytes(out, "messages.1.content").String())
		require.Equal(t, "get_weather", gjson.GetBytes(out, "tools.0.function.name").String())
	})

	t.Run("absent content key unchanged", func(t *testing.T) {
		in := []byte(`{"messages":[{"role":"system"},{"role":"user","content":"hi"}]}`)
		out, err := normalizeVolcKimiChatRequest(in)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, "messages.0.content").Exists())
		require.Equal(t, "hi", gjson.GetBytes(out, "messages.1.content").String())
	})

	// Body without empty parts is returned byte-for-byte.
	clean := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"a"}]},{"role":"user","content":"hi"}]}`)
	out, err := normalizeVolcKimiChatRequest(clean)
	require.NoError(t, err)
	require.Equal(t, string(clean), string(out))

	// Selected-account routing applies it; other suppliers keep the part.
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"system","content":[{"type":"text","text":""},{"type":"text","text":"rules"}]},{"role":"user","content":"hi"}]}`)
	volc, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", body)
	require.NoError(t, err)
	require.EqualValues(t, 1, gjson.GetBytes(volc, "messages.0.content.#").Int())
	other, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", body)
	require.NoError(t, err)
	require.EqualValues(t, 2, gjson.GetBytes(other, "messages.0.content.#").Int())
}

// cn-prod 2026-11-18: Volc rejects messages.content.type thinking / reasoning_text /
// audio_url / bare type=video. Drop reasoning parts; rewrite video; reject audio.
func TestVolcKimiContentPartCompatibility(t *testing.T) {
	dropped, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"hi"},{"type":"reasoning_text","text":"raw"}]},{"role":"user","content":"next"}]}`))
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"text","text":"hi"}]`, gjson.GetBytes(dropped, "messages.0.content").Raw)

	cacheBreakpoint, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"prompt_cache_breakpoint"}]}]}`))
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"text","text":"hi"}]`, gjson.GetBytes(cacheBreakpoint, "messages.1.content").Raw)

	// Lone object content must wrap then drop (same as thinking/reasoning_text).
	loneCache, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":{"type":"prompt_cache_breakpoint"}}]}`))
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(loneCache, "messages.0.content").String())

	onlyReasoning, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x"}]},{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(onlyReasoning, "messages.0.content").String())

	// Reasoning-only interleaved assistant between tool_calls and tool is dropped
	// after parts are emptied (open tool round-trip).
	interleavedReasoning := []byte(`{"messages":[{"role":"user","content":"u"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"assistant","content":[{"type":"thinking","thinking":"…"}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	fixed, err := normalizeVolcKimiChatRequest(interleavedReasoning)
	require.NoError(t, err)
	require.EqualValues(t, 3, gjson.GetBytes(fixed, "messages.#").Int())
	require.Equal(t, "tool", gjson.GetBytes(fixed, "messages.2.role").String())

	videoType, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"video","url":"https://example.com/a.mp4"},{"type":"text","text":"see"}]}]}`))
	require.NoError(t, err)
	require.Equal(t, "video_url", gjson.GetBytes(videoType, "messages.0.content.0.type").String())
	require.Equal(t, "https://example.com/a.mp4", gjson.GetBytes(videoType, "messages.0.content.0.video_url.url").String())
	require.False(t, gjson.GetBytes(videoType, "messages.0.content.0.url").Exists())

	videoNested, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"video","video":{"url":"ms://file-9"}}]}]}`))
	require.NoError(t, err)
	require.Equal(t, "video_url", gjson.GetBytes(videoNested, "messages.0.content.0.type").String())
	require.Equal(t, "ms://file-9", gjson.GetBytes(videoNested, "messages.0.content.0.video_url.url").String())
	require.False(t, gjson.GetBytes(videoNested, "messages.0.content.0.video").Exists())

	for _, audioBody := range []string{
		`{"messages":[{"role":"user","content":[{"type":"audio_url","audio_url":{"url":"https://example.com/a.wav"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA=="}}]}]}`,
		`{"messages":[{"role":"user","content":{"type":"audio_url","audio_url":{"url":"https://example.com/a.wav"}}}]}`,
	} {
		_, err := normalizeVolcKimiChatRequest([]byte(audioBody))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "messages.content", invalid.Param)
		require.Equal(t, volcKimiAudioUnsupportedMessage, invalid.Message)
		require.NotContains(t, strings.ToLower(invalid.Message), "volc")
	}

	// Top-level thinking.type is unchanged by content-part drops.
	thinking, err := normalizeVolcKimiChatRequest([]byte(`{"thinking":{"type":"enabled"},"messages":[{"role":"user","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"hi"}]}]}`))
	require.NoError(t, err)
	require.Equal(t, "enabled", gjson.GetBytes(thinking, "thinking.type").String())
	require.JSONEq(t, `[{"type":"text","text":"hi"}]`, gjson.GetBytes(thinking, "messages.0.content").Raw)

	// Orphan tool_call_id still 400 (KVV); not rewritten.
	_, err = normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"missing","content":"x"}]}`))
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "messages", invalid.Param)

	// Selected-account routing applies content-part rules; other suppliers keep parts.
	reasoningBody := []byte(`{"model":"kimi-k3","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"hi"}]}]}`)
	volc, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", reasoningBody)
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"text","text":"hi"}]`, gjson.GetBytes(volc, "messages.0.content").Raw)
	other, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", reasoningBody)
	require.NoError(t, err)
	require.EqualValues(t, 2, gjson.GetBytes(other, "messages.0.content.#").Int())
	require.Equal(t, "thinking", gjson.GetBytes(other, "messages.0.content.0.type").String())

	audioBody := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"audio_url","audio_url":{"url":"https://example.com/a.wav"}}]}]}`)
	_, err = applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", audioBody)
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "messages.content", invalid.Param)
	keptAudio, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", audioBody)
	require.NoError(t, err)
	require.Equal(t, "audio_url", gjson.GetBytes(keptAudio, "messages.0.content.0.type").String())
}

// cn-prod 2026-11-19 rt@tk.com: type=file missing nested file (vap-1105-51) and
// non-PDF file_data (video/mp4 + text/plain on vap-1106-4) → Volc 400. Normalize
// without conflicting with KVV fail-closed / video / text paths.
func TestVolcKimiFileContentPartCompatibility(t *testing.T) {
	t.Run("missing_file_object_local_400", func(t *testing.T) {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"file"},{"type":"text","text":"hi"}]}]}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "messages.content.file", invalid.Param)
		require.Equal(t, volcKimiFileMissingMessage, invalid.Message)
		require.NotContains(t, strings.ToLower(invalid.Message), "volc")
	})

	t.Run("empty_file_object_local_400", func(t *testing.T) {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{}}]}]}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "messages.content.file", invalid.Param)
		require.Equal(t, volcKimiFileXorMessage, invalid.Message)
	})

	t.Run("multiple_file_sources_local_400", func(t *testing.T) {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-1","file_url":"https://example.com/a.pdf"}}]}]}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, volcKimiFileXorMessage, invalid.Message)
	})

	t.Run("video_mp4_file_data_rewrites_to_video_url", func(t *testing.T) {
		dataURL := "data:video/mp4;base64," + strings.Repeat("A", ((volcKimiMinDataVideoBytes+2)/3)*4)
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"` + dataURL + `","filename":"a.mp4"}},{"type":"text","text":"see"}]}]}`)
		out, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.Equal(t, "video_url", gjson.GetBytes(out, "messages.0.content.0.type").String())
		require.Equal(t, dataURL, gjson.GetBytes(out, "messages.0.content.0.video_url.url").String())
		require.False(t, gjson.GetBytes(out, "messages.0.content.0.file").Exists())
		require.Equal(t, "see", gjson.GetBytes(out, "messages.0.content.1.text").String())
	})

	t.Run("text_plain_file_data_promotes_to_text", func(t *testing.T) {
		plain := base64.StdEncoding.EncodeToString([]byte("The secret word in the file is orchid."))
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"what is the secret?"},{"type":"file","file":{"file_data":"data:text/plain;base64,` + plain + `","filename":"secret.txt"}}]}]}`)
		out, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.Equal(t, "text", gjson.GetBytes(out, "messages.0.content.1.type").String())
		require.Equal(t, "The secret word in the file is orchid.", gjson.GetBytes(out, "messages.0.content.1.text").String())
		require.False(t, gjson.GetBytes(out, "messages.0.content.1.file").Exists())
	})

	t.Run("pdf_file_data_passthrough", func(t *testing.T) {
		pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 minimal"))
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,` + pdf + `","filename":"a.pdf"}},{"type":"text","text":"summarize"}]}]}`)
		out, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.Equal(t, "file", gjson.GetBytes(out, "messages.0.content.0.type").String())
		require.Equal(t, "data:application/pdf;base64,"+pdf, gjson.GetBytes(out, "messages.0.content.0.file.file_data").String())
		require.Equal(t, "a.pdf", gjson.GetBytes(out, "messages.0.content.0.file.filename").String())
	})

	t.Run("file_id_passthrough", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-abc"}},{"type":"text","text":"hi"}]}]}`)
		out, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.JSONEq(t, string(body), string(out))
	})

	t.Run("flat_file_url_nests_and_non_pdf_ext_400", func(t *testing.T) {
		_, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"file","file_url":"https://cdn.example/a.txt"}]}]}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, volcKimiFilePDFOnlyMessage, invalid.Message)
	})

	t.Run("http_video_file_url_rewrites_to_video_url", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_url":"https://cdn.example/clip.mp4"}}]}]}`)
		out, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.Equal(t, "video_url", gjson.GetBytes(out, "messages.0.content.0.type").String())
		require.Equal(t, "https://cdn.example/clip.mp4", gjson.GetBytes(out, "messages.0.content.0.video_url.url").String())
	})

	t.Run("image_file_data_local_400", func(t *testing.T) {
		img := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
		_, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:image/png;base64,` + img + `","filename":"a.png"}}]}]}`))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, volcKimiFilePDFOnlyMessage, invalid.Message)
		require.Equal(t, "messages.content.file", invalid.Param)
	})

	t.Run("non_file_type_with_nested_file_untouched", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","file":{"file_id":"file-x"}}]}]}`)
		out, err := normalizeVolcKimiChatRequest(body)
		require.NoError(t, err)
		require.JSONEq(t, string(body), string(out))
	})

	t.Run("selected_account_applies_non_volc_keeps", func(t *testing.T) {
		missing := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"file"},{"type":"text","text":"hi"}]}]}`)
		_, err := applyMoonshotThinkingShape(nil, volcEnginePlanTestAccount(), "kimi-k3", missing)
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid)
		require.Equal(t, "messages.content.file", invalid.Param)
		kept, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", missing)
		require.NoError(t, err)
		require.Equal(t, "file", gjson.GetBytes(kept, "messages.0.content.0.type").String())
		require.False(t, gjson.GetBytes(kept, "messages.0.content.0.file").Exists())
	})
}
