//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type cursorLiveTransport struct {
	protocolTargetHTTPUpstream
	client *http.Client
}

func (u *cursorLiveTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}

// Opt-in integration probe of the actual native adapter and registered converters.
// It neither modifies production scheduling nor proves production usage attribution.
// Stop at the first failure, including quota exhaustion; never cascade into retries.
func TestCursorMessagesConvertersLive(t *testing.T) {
	path := os.Getenv("TOKENKEY_CURSOR_ACCOUNT_FILE")
	if path == "" {
		t.Skip("requires an explicit protected account credentials file")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved struct {
		Credentials map[string]any `json:"credentials"`
	}
	require.NoError(t, json.Unmarshal(raw, &saved))
	model := os.Getenv("TOKENKEY_CURSOR_LIVE_MODEL")
	if model == "" {
		model = "claude-opus-5"
	}
	account := cursorCandidateAccount(model)
	account.Credentials = saved.Credentials
	attachTestProtocolCapability(account, protocolrouter.ProtocolMessages)
	transport := &http.Transport{ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	svc := protocolTargetTestService(nil)
	svc.httpUpstream = &cursorLiveTransport{client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	call := func(inbound protocolrouter.Protocol, payload map[string]any) []byte {
		t.Helper()
		time.Sleep(2 * time.Second)
		payload["model"] = model
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		stream, _ := payload["stream"].(bool)
		path := protocolrouter.ResponsesPathNone
		if inbound == protocolrouter.ProtocolResponses {
			path = protocolrouter.ResponsesPathRoot
		}
		canonical, err := protocolrouter.ParseCanonicalRequest(inbound, path, model, stream, body)
		require.NoError(t, err)
		router := NewProtocolRouter()
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		ctx = WithProtocolRouting(ctx, router, canonical)
		plan, _, err := protocolPlanForAccount(ctx, account, model)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, protocolRouteContractInboundPath(inbound), bytes.NewReader(body)).WithContext(ctx)
		var result *OpenAIForwardResult
		_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
			func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account),
			protocolExecutorsForTest(plan, func(ctx context.Context, a *Account, _ protocolrouter.Plan, r protocolrouter.CanonicalRequest) (any, error) {
				var err error
				switch inbound {
				case protocolrouter.ProtocolMessages:
					result, err = svc.ForwardAsAnthropic(ctx, c, a, r.Body(), "", "")
				case protocolrouter.ProtocolChatCompletions:
					result, err = svc.ForwardAsChatCompletions(ctx, c, a, r.Body(), "", "")
				default:
					result, err = svc.Forward(ctx, c, a, r.Body())
				}
				return result, err
			}))
		require.NoError(t, err, "protocol=%s stream=%t", inbound, stream)
		require.Equal(t, http.StatusOK, recorder.Code)
		require.NotNil(t, result)
		require.NotEmpty(t, result.BillingTier)
		require.Positive(t, result.Usage.OutputTokens)
		if !stream {
			require.True(t, json.Valid(recorder.Body.Bytes()))
		}
		t.Logf("model=%s protocol=%s stream=%t tier=%s input=%d output=%d", model, inbound, stream, result.BillingTier, result.Usage.InputTokens, result.Usage.OutputTokens)
		if stream {
			return cursorLiveBufferedSSE(t, inbound, recorder.Body.Bytes())
		}
		return recorder.Body.Bytes()
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}
	prompt := "Call read_fixture with path fixture.txt. After receiving the result, return its exact contents. Do not guess."
	for _, inbound := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		if selected := os.Getenv("TOKENKEY_CURSOR_LIVE_PROTOCOL"); selected != "" && selected != string(inbound) {
			continue
		}
		for _, stream := range []bool{false, true} {
			payload := map[string]any{"stream": stream, "max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": "Reply exactly CURSOR_TEXT_OK."}}}
			if inbound == protocolrouter.ProtocolResponses {
				delete(payload, "messages")
				payload["input"] = "Reply exactly CURSOR_TEXT_OK."
			}
			require.Contains(t, string(call(inbound, payload)), "CURSOR_TEXT_OK")
			history := []any{map[string]any{"role": "user", "content": prompt}}
			switch inbound {
			case protocolrouter.ProtocolMessages:
				payload["messages"] = history
				payload["tools"] = []any{map[string]any{"name": "read_fixture", "description": "Read a local fixture.", "input_schema": schema}}
			case protocolrouter.ProtocolChatCompletions:
				payload["messages"] = history
				payload["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_fixture", "description": "Read a local fixture.", "parameters": schema}}}
			default:
				payload["input"] = history
				payload["tools"] = []any{map[string]any{"type": "function", "name": "read_fixture", "description": "Read a local fixture.", "parameters": schema}}
			}
			first := call(inbound, payload)
			nonce := "CURSOR_TOOL_" + uuid.NewString()
			switch inbound {
			case protocolrouter.ProtocolMessages:
				content := gjson.GetBytes(first, "content")
				calls := gjson.GetBytes(first, `content.#(type=="tool_use")#`).Array()
				require.Len(t, calls, 1)
				require.Equal(t, "read_fixture", calls[0].Get("name").String())
				require.Equal(t, "fixture.txt", calls[0].Get("input.path").String())
				payload["messages"] = append(history, map[string]any{"role": "assistant", "content": json.RawMessage(content.Raw)}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": calls[0].Get("id").String(), "content": nonce}}})
			case protocolrouter.ProtocolChatCompletions:
				message := gjson.GetBytes(first, "choices.0.message")
				calls := message.Get("tool_calls").Array()
				require.Len(t, calls, 1)
				require.Equal(t, "read_fixture", calls[0].Get("function.name").String())
				require.Equal(t, "fixture.txt", gjson.Get(calls[0].Get("function.arguments").String(), "path").String())
				payload["messages"] = append(history, json.RawMessage(message.Raw), map[string]any{"role": "tool", "tool_call_id": calls[0].Get("id").String(), "content": nonce})
			default:
				output := gjson.GetBytes(first, "output").Array()
				calls := gjson.GetBytes(first, `output.#(type=="function_call")#`).Array()
				require.Len(t, calls, 1)
				require.Equal(t, "read_fixture", calls[0].Get("name").String())
				require.Equal(t, "fixture.txt", gjson.Get(calls[0].Get("arguments").String(), "path").String())
				for _, item := range output {
					history = append(history, json.RawMessage(item.Raw))
				}
				payload["input"] = append(history, map[string]any{"type": "function_call_output", "call_id": calls[0].Get("call_id").String(), "output": nonce})
			}

			final := call(inbound, payload)
			var answer strings.Builder
			switch inbound {
			case protocolrouter.ProtocolMessages:
				for _, part := range gjson.GetBytes(final, "content").Array() {
					if part.Get("type").String() == "text" {
						answer.WriteString(part.Get("text").String())
					}
				}
			case protocolrouter.ProtocolChatCompletions:
				answer.WriteString(gjson.GetBytes(final, "choices.0.message.content").String())
			default:
				for _, item := range gjson.GetBytes(final, "output").Array() {
					for _, part := range item.Get("content").Array() {
						if part.Get("type").String() == "output_text" {
							answer.WriteString(part.Get("text").String())
						}
					}
				}
			}
			require.Contains(t, answer.String(), nonce)
		}
	}
}

// Reconstruct only complete public SSE output so the same assertions cover both
// response modes. A terminal error or missing completion cannot count as success.
func cursorLiveBufferedSSE(t *testing.T, protocol protocolrouter.Protocol, raw []byte) []byte {
	t.Helper()
	blocks := map[int]map[string]any{}
	args := map[int]string{}
	chatCalls := map[int]map[string]any{}
	var content strings.Builder
	var response json.RawMessage
	completed := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			if protocol == protocolrouter.ProtocolChatCompletions {
				completed = true
			}
			continue
		}
		require.True(t, json.Valid([]byte(data)))
		e := gjson.Parse(data)
		require.NotEqual(t, "error", e.Get("type").String())
		require.False(t, e.Get("error").Exists())
		switch protocol {
		case protocolrouter.ProtocolMessages:
			i := int(e.Get("index").Int())
			switch e.Get("type").String() {
			case "content_block_start":
				var b map[string]any
				require.NoError(t, json.Unmarshal([]byte(e.Get("content_block").Raw), &b))
				blocks[i] = b
			case "content_block_delta":
				switch e.Get("delta.type").String() {
				case "text_delta":
					old, _ := blocks[i]["text"].(string)
					blocks[i]["text"] = old + e.Get("delta.text").String()
				case "input_json_delta":
					args[i] += e.Get("delta.partial_json").String()
				}
			case "message_stop":
				completed = true
			}
		case protocolrouter.ProtocolChatCompletions:
			delta := e.Get("choices.0.delta")
			content.WriteString(delta.Get("content").String())
			for _, call := range delta.Get("tool_calls").Array() {
				i := int(call.Get("index").Int())
				if chatCalls[i] == nil {
					chatCalls[i] = map[string]any{"type": "function", "function": map[string]any{}}
				}
				c := chatCalls[i]
				if id := call.Get("id").String(); id != "" {
					c["id"] = id
				}
				fn := c["function"].(map[string]any)
				if name := call.Get("function.name").String(); name != "" {
					fn["name"] = name
				}
				args[i] += call.Get("function.arguments").String()
				fn["arguments"] = args[i]
			}
		default:
			require.NotEqual(t, "response.failed", e.Get("type").String())
			if e.Get("type").String() == "response.completed" {
				completed = true
				response = json.RawMessage(e.Get("response").Raw)
			}
		}
	}
	require.True(t, completed, "stream must include its successful terminal marker")
	var result any
	switch protocol {
	case protocolrouter.ProtocolMessages:
		list := []any{}
		for i := 0; i < len(blocks); i++ {
			b := blocks[i]
			require.NotNil(t, b)
			if b["type"] == "tool_use" && args[i] != "" {
				var input any
				require.NoError(t, json.Unmarshal([]byte(args[i]), &input))
				b["input"] = input
			}
			list = append(list, b)
		}
		result = map[string]any{"role": "assistant", "content": list}
	case protocolrouter.ProtocolChatCompletions:
		calls := []any{}
		for i := 0; i < len(chatCalls); i++ {
			require.NotNil(t, chatCalls[i])
			calls = append(calls, chatCalls[i])
		}
		result = map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content.String(), "tool_calls": calls}}}}
	default:
		require.NotEmpty(t, response)
		result = response
	}
	out, err := json.Marshal(result)
	require.NoError(t, err)
	return out
}
