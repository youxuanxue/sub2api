//go:build unit

package service

import (
	"context"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
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
	got := applyMoonshotK3DynamicToolsShape("kimi-k3", body)
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
	for _, msg := range msgs {
		if msg.Get("tools").Exists() {
			t.Fatalf("message-level tools must be stripped, got %s", msg.Raw)
		}
	}
}

func TestApplyMoonshotK3DynamicToolsShape_KeepsContentBearingSystemTools(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3",
		"messages":[
			{"role":"system","content":"keep me","tools":[{"type":"function","function":{"name":"Calculator"}}]}
		]
	}`)
	got := applyMoonshotK3DynamicToolsShape("kimi-k3", body)
	if string(got) != string(body) {
		t.Fatalf("system+tools with content must stay unchanged, got %s", got)
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
	got := applyMoonshotK3DynamicToolsShape("kimi-k3", body)
	names := []string{}
	for _, tool := range gjson.GetBytes(got, "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	if len(names) != 2 || names[0] != "Weather" || names[1] != "Calculator" {
		t.Fatalf("tools=%v want [Weather Calculator]", names)
	}
}

func TestApplyMoonshotK3DynamicToolsShape_LeavesOtherModels(t *testing.T) {
	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"system","tools":[{"type":"function","function":{"name":"Calculator"}}]}]}`)
	got := applyMoonshotK3DynamicToolsShape("kimi-k2.5", body)
	if string(got) != string(body) {
		t.Fatalf("non-k3 body must be unchanged")
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
	if gjson.GetBytes(capturedBody, "messages.#").Int() != 2 {
		t.Fatalf("messages len=%d want 2", gjson.GetBytes(capturedBody, "messages.#").Int())
	}
	for _, msg := range gjson.GetBytes(capturedBody, "messages").Array() {
		if msg.Get("tools").Exists() {
			t.Fatalf("message-level tools must be promoted away, got %s", msg.Raw)
		}
	}
}
