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

	orphan, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`))
	require.NoError(t, err)
	require.Equal(t, "user", gjson.GetBytes(orphan, "messages.1.role").String())
	require.False(t, gjson.GetBytes(orphan, "messages.1.tool_call_id").Exists())
	require.Equal(t, "result", gjson.GetBytes(orphan, "messages.1.content").String())

	matched, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`))
	require.NoError(t, err)
	require.Equal(t, "tool", gjson.GetBytes(matched, "messages.1.role").String())
	require.Equal(t, "call_1", gjson.GetBytes(matched, "messages.1.tool_call_id").String())

	empty, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"","parameters":{"type":"object"}}},{"type":"function","function":{"name":"  ","parameters":{"type":"object"}}}],"tool_choice":"required"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(empty, "tools").Exists())
	require.False(t, gjson.GetBytes(empty, "tool_choice").Exists())

	kept, err := normalizeVolcKimiChatRequest([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"","parameters":{"type":"object"}}},{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`))
	require.NoError(t, err)
	require.EqualValues(t, 1, gjson.GetBytes(kept, "tools.#").Int())
	require.Equal(t, "lookup", gjson.GetBytes(kept, "tools.0.function.name").String())
	require.Equal(t, "auto", gjson.GetBytes(kept, "tool_choice").String())

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
		for _, rawURL := range []string{"data:video/mp4;base64,AAAA", "ms://video-1"} {
			body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"` + rawURL + `"}}]}]}`)
			got, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", body)
			require.NoError(t, err)
			require.Equal(t, rawURL, gjson.GetBytes(got, "messages.0.content.0.video_url.url").String())
		}
		for _, rawURL := range []string{"", "data:video/mp4;base64,", "http://127.0.0.1/secret.mp4", "http://169.254.169.254/latest/meta-data"} {
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
