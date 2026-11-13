//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyMoonshotK3DynamicToolsShape_PromotesSystemTools(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3",
		"messages":[
			{"role":"system","content":"你是一个乐于助人的助手。"},
			{"role":"user","content":"帮我计算一下 23 * 47 的结果。"},
			{"role":"system","tools":[{"type":"function","function":{"name":"Calculator","description":"计算器","parameters":{"type":"object","properties":{"expr":{"type":"string"}},"required":["expr"]}}}]}
		],
		"stream":true
	}`)
	got := mustApplyMoonshotK3DynamicTools(t, "kimi-k3", body)
	msgs := gjson.GetBytes(got, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages len=%d want 2 (dynamic tools system dropped)", len(msgs))
	}
	if msgs[0].Get("role").String() != "system" || msgs[0].Get("content").String() == "" {
		t.Fatalf("first message must remain textual system, got %s", msgs[0].Raw)
	}
	if msgs[1].Get("role").String() != "user" {
		t.Fatalf("second message role=%q want user", msgs[1].Get("role").String())
	}
	tools := gjson.GetBytes(got, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools len=%d want 1", len(tools))
	}
	if tools[0].Get("function.name").String() != "Calculator" {
		t.Fatalf("tool name=%q want Calculator", tools[0].Get("function.name").String())
	}
	if gjson.GetBytes(got, "tool_choice").String() != "required" {
		t.Fatalf("tool_choice=%s want required", gjson.GetBytes(got, "tool_choice").Raw)
	}
	for _, msg := range msgs {
		if msg.Get("tools").Exists() {
			t.Fatalf("message-level tools must be stripped, got %s", msg.Raw)
		}
	}
}

func TestApplyMoonshotK3DynamicToolsShape_RejectsContentBearingSystemTools(t *testing.T) {
	cases := []string{
		`{"role":"system","content":"keep me","tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"system","content":"  x","tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"system","content":[{"type":"text","text":"hi"}],"tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"system","content":{"type":"text","text":"hi"},"tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"system","content":[],"tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"system","content":"keep me","tools":[]}`,
		`{"role":"system","content":"keep me","tools":{"type":"function","function":{"name":"Calculator"}}}`,
	}
	for _, msg := range cases {
		body := []byte(`{"model":"kimi-k3","messages":[` + msg + `]}`)
		requireMoonshotK3DynamicToolsParam(t, "kimi-k3", body, "messages")
	}
}

func TestApplyMoonshotK3DynamicToolsShape_PromotesEmptyStringContent(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3",
		"messages":[
			{"role":"user","content":"算 1+1"},
			{"role":"system","content":"","tools":[{"type":"function","function":{"name":"Calculator"}}]}
		]
	}`)
	got := mustApplyMoonshotK3DynamicTools(t, "kimi-k3", body)
	if gjson.GetBytes(got, "messages.#").Int() != 1 {
		t.Fatalf("messages len=%d want 1", gjson.GetBytes(got, "messages.#").Int())
	}
	if gjson.GetBytes(got, "tools.0.function.name").String() != "Calculator" {
		t.Fatalf("tools not promoted: %s", got)
	}
	if gjson.GetBytes(got, "tool_choice").String() != "required" {
		t.Fatalf("tool_choice=%s want required", gjson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestApplyMoonshotK3DynamicToolsShape_MergesExistingTopLevelTools(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3",
		"tools":[{"type":"function","function":{"name":"Weather"}}],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"system","tools":[{"type":"function","function":{"name":"Calculator"}}]}
		]
	}`)
	got := mustApplyMoonshotK3DynamicTools(t, "kimi-k3", body)
	names := []string{}
	for _, tool := range gjson.GetBytes(got, "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	if len(names) != 2 || names[0] != "Weather" || names[1] != "Calculator" {
		t.Fatalf("tools=%v want [Weather Calculator]", names)
	}
	if gjson.GetBytes(got, "tool_choice").String() != "required" {
		t.Fatalf("tool_choice=%s want required", gjson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestApplyMoonshotK3DynamicToolsShape_KeepsExplicitToolChoice(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3",
		"tool_choice":"auto",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"system","tools":[{"type":"function","function":{"name":"Calculator"}}]}
		]
	}`)
	got := mustApplyMoonshotK3DynamicTools(t, "kimi-k3", body)
	if gjson.GetBytes(got, "tool_choice").String() != "auto" {
		t.Fatalf("explicit tool_choice must stay, got %s", gjson.GetBytes(got, "tool_choice").Raw)
	}
	if gjson.GetBytes(got, "tools.0.function.name").String() != "Calculator" {
		t.Fatalf("tools not promoted: %s", got)
	}
}

func TestApplyMoonshotK3DynamicToolsShape_LeavesOtherModels(t *testing.T) {
	body := []byte(`{"model":"moonshot-v1-8k","messages":[{"role":"user","content":"hi","tools":[{"type":"function","function":{"name":"Calculator"}}]}]}`)
	got, err := applyMoonshotK3DynamicToolsShape("moonshot-v1-8k", body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(got))
}

func TestApplyMoonshotK3DynamicToolsShape_CoversOtherKimiIDs(t *testing.T) {
	for _, model := range []string{"kimi-k2.5", "kimi-k30", "kimi-k3.1-pro"} {
		body := []byte(`{"model":"` + model + `","messages":[{"role":"system","tools":[{"type":"function","function":{"name":"Calculator"}}]},{"role":"user","content":"hi"}]}`)
		got := mustApplyMoonshotK3DynamicTools(t, model, body)
		require.Equal(t, "Calculator", gjson.GetBytes(got, "tools.0.function.name").String())
		require.Equal(t, "required", gjson.GetBytes(got, "tool_choice").String())
		userTools := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi","tools":[{"type":"function","function":{"name":"Calculator"}}]}]}`)
		requireMoonshotK3DynamicToolsParam(t, model, userTools, "messages")
	}
}

func TestApplyMoonshotK3DynamicToolsShape_PromotesKimiK31Pro(t *testing.T) {
	body := []byte(`{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"算 1+1"},{"role":"system","tools":[{"type":"function","function":{"name":"Calculator"}}]}]}`)
	got := mustApplyMoonshotK3DynamicTools(t, "kimi-k3.1-pro", body)
	if gjson.GetBytes(got, "messages.#").Int() != 1 {
		t.Fatalf("messages len=%d want 1", gjson.GetBytes(got, "messages.#").Int())
	}
	if gjson.GetBytes(got, "tools.0.function.name").String() != "Calculator" {
		t.Fatalf("tools not promoted: %s", got)
	}
	if gjson.GetBytes(got, "tool_choice").String() != "required" {
		t.Fatalf("tool_choice=%s want required", gjson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestForwardAsChatCompletionsDispatched_PromotesKimiK3DynamicTools(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{Model: "kimi-k3"}, nil
	}

	account := &Account{
		ID:          111,
		Platform:    PlatformNewAPI,
		ChannelType: 17,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
			"model_mapping": map[string]any{
				"kimi-k3": "kimi-k3",
			},
		},
	}
	svc := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(nil)
	body := []byte(`{
		"model":"kimi-k3",
		"messages":[
			{"role":"system","content":"助手"},
			{"role":"user","content":"算 23*47"},
			{"role":"system","tools":[{"type":"function","function":{"name":"Calculator","parameters":{"type":"object","properties":{"expr":{"type":"string"}},"required":["expr"]}}}]}
		],
		"stream":true
	}`)
	if _, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", ""); err != nil {
		t.Fatalf("ForwardAsChatCompletionsDispatched: %v", err)
	}
	if gjson.GetBytes(capturedBody, "tools.#").Int() != 1 {
		t.Fatalf("tools len=%d want 1", gjson.GetBytes(capturedBody, "tools.#").Int())
	}
	if gjson.GetBytes(capturedBody, "tools.0.function.name").String() != "Calculator" {
		t.Fatalf("tool=%q want Calculator", gjson.GetBytes(capturedBody, "tools.0.function.name").String())
	}
	if gjson.GetBytes(capturedBody, "tool_choice").String() != "required" {
		t.Fatalf("tool_choice=%s want required", gjson.GetBytes(capturedBody, "tool_choice").Raw)
	}
	if gjson.GetBytes(capturedBody, "messages.#").Int() != 2 {
		t.Fatalf("messages len=%d want 2", gjson.GetBytes(capturedBody, "messages.#").Int())
	}
	for _, msg := range gjson.GetBytes(capturedBody, "messages").Array() {
		if msg.Get("tools").Exists() {
			t.Fatalf("message-level tools must be promoted away, got %s", msg.Raw)
		}
	}
}

func TestApplyMoonshotK3DynamicToolsShape_PromotesEmptyContentAndMergesDistinctNames(t *testing.T) {
	longName := strings.Repeat("a", 128)
	body := []byte(`{
		"model":"kimi-k3",
		"tools":[{"type":"function","function":{"name":"Weather"}}],
		"messages":[
			{"role":"system","content":null,"tools":[{"type":"function","function":{"name":"Calculator"}}]},
			{"role":"user","content":"hi"},
			{"role":"System","content":"   \n\t","tools":[{"type":"function","function":{"name":"` + longName + `"}}]}
		]
	}`)
	got, err := applyMoonshotK3DynamicToolsShape("kimi-k3", body)
	require.NoError(t, err)
	msgs := gjson.GetBytes(got, "messages").Array()
	require.Len(t, msgs, 1)
	require.Equal(t, "user", msgs[0].Get("role").String())
	require.Equal(t, "hi", msgs[0].Get("content").String())
	require.False(t, msgs[0].Get("tools").Exists())
	tools := gjson.GetBytes(got, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "Weather", tools[0].Get("function.name").String())
	require.Equal(t, "Calculator", tools[1].Get("function.name").String())
	require.Equal(t, longName, tools[2].Get("function.name").String())
	require.Equal(t, "required", gjson.GetBytes(got, "tool_choice").String())
}

func TestApplyMoonshotK3DynamicToolsShape_PromotesBlankFunctionName(t *testing.T) {
	for _, name := range []string{"", "   "} {
		body := []byte(`{"model":"kimi-k3","messages":[{"role":"system","content":"","tools":[{"type":"function","function":{"name":"` + name + `"}}]},{"role":"user","content":"hello"}]}`)
		got, err := applyMoonshotK3DynamicToolsShape("kimi-k3", body)
		require.NoError(t, err)
		require.Equal(t, name, gjson.GetBytes(got, "tools.0.function.name").String())
		require.Equal(t, "user", gjson.GetBytes(got, "messages.0.role").String())
		require.False(t, gjson.GetBytes(got, "messages.0.tools").Exists())
	}
}

func TestApplyMoonshotK3DynamicToolsShape_RejectsToolsOnNonSystemMessages(t *testing.T) {
	for _, msg := range []string{
		`{"role":"user","content":"hi","tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"assistant","content":"hi","tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"tool","content":"result","tools":[{"type":"function","function":{"name":"Calculator"}}]}`,
		`{"role":"user","content":"hi","tools":{"type":"function","function":{"name":"Calculator"}}}`,
		`{"role":"assistant","content":"hi","tools":null}`,
	} {
		body := []byte(`{"model":"kimi-k3","messages":[` + msg + `]}`)
		requireMoonshotK3DynamicToolsParam(t, "kimi-k3", body, "messages")
	}
	pro := []byte(`{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"hi","tools":[{"type":"function","function":{"name":"Calculator"}}]}]}`)
	requireMoonshotK3DynamicToolsParam(t, "kimi-k3.1-pro", pro, "messages")
}

func TestApplyMoonshotK3DynamicToolsShape_RejectsDynamicToolsMissingFields(t *testing.T) {
	for _, tools := range []string{
		`[{"function":{"name":"Calculator"}}]`,
		`[{"type":null,"function":{"name":"Calculator"}}]`,
		`[{"type":"function"}]`,
		`[{"type":"function","function":null}]`,
		`[{"type":"function","function":{"description":"calc"}}]`,
		`[{"type":"function","function":{"name":null}}]`,
	} {
		body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"},{"role":"system","content":"","tools":` + tools + `}]}`)
		requireMoonshotK3DynamicToolsParam(t, "kimi-k3", body, "tools")
	}
}

func TestApplyMoonshotK3DynamicToolsShape_RejectsMalformedMessageTools(t *testing.T) {
	for _, msg := range []string{
		`{"role":"system","content":null,"tools":{"type":"function","function":{"name":"Calculator"}}}`,
		`{"role":"system","tools":null}`,
		`{"role":"system","content":"","tools":[null]}`,
		`{"role":"system","content":" ","tools":["Calculator"]}`,
		`{"role":"system","tools":[1]}`,
	} {
		body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"},` + msg + `]}`)
		requireMoonshotK3DynamicToolsParam(t, "kimi-k3", body, "tools")
	}
}

func TestApplyMoonshotK3DynamicToolsShape_RejectsDuplicateFunctionNames(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"model":"kimi-k3","messages":[{"role":"system","tools":[{"type":"function","function":{"name":"calc"}},{"type":"function","function":{"name":"calc"}}]},{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"kimi-k3","messages":[{"role":"system","tools":[{"type":"function","function":{"name":"calc"}}]},{"role":"system","content":"","tools":[{"type":"function","function":{"name":" calc "}}]},{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"kimi-k3","tools":[{"type":"function","function":{"name":" calc "}}],"messages":[{"role":"system","tools":[{"type":"function","function":{"name":"calc"}}]},{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"kimi-k3","tools":[{"type":"function","name":"calc"}],"messages":[{"role":"system","content":null,"tools":[{"type":"function","function":{"name":"calc"}}]},{"role":"user","content":"hi"}]}`),
	}
	for _, body := range bodies {
		requireMoonshotK3DynamicToolsParam(t, "kimi-k3", body, "tools")
	}
}

func TestForwardAsChatCompletionsDispatched_RejectsKimiK3ToolsOnUserMessage(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	called := false
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, _ bridge.ChannelContextInput, _ []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		called = true
		return &bridge.DispatchOutcome{Model: "kimi-k3"}, nil
	}

	account := &Account{
		ID:          111,
		Platform:    PlatformNewAPI,
		ChannelType: 17,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
			"model_mapping": map[string]any{
				"kimi-k3": "kimi-k3",
			},
		},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi","tools":[{"type":"function","function":{"name":"Calculator"}}]}]}`)
	_, err := (&OpenAIGatewayService{}).ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "messages", invalid.Param)
	require.Equal(t, "invalid_parameter", invalid.Code)
	require.NotContains(t, strings.ToLower(invalid.Message), "volc")
	require.False(t, called)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"param":"messages"`)
	require.NotContains(t, strings.ToLower(rec.Body.String()), "volc")
}

func mustApplyMoonshotK3DynamicTools(t *testing.T, model string, body []byte) []byte {
	t.Helper()
	got, err := applyMoonshotK3DynamicToolsShape(model, body)
	require.NoError(t, err)
	return got
}

func requireMoonshotK3DynamicToolsParam(t *testing.T, model string, body []byte, param string) {
	t.Helper()
	got, err := applyMoonshotK3DynamicToolsShape(model, body)
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, param, invalid.Param)
	require.Equal(t, "invalid_parameter", invalid.Code)
	lower := strings.ToLower(invalid.Message)
	for _, forbidden := range []string{"volc", "volcano", "火山", "upstream supplier", "账号", "渠道"} {
		require.NotContains(t, lower, strings.ToLower(forbidden))
	}
	require.JSONEq(t, string(body), string(got))
}
