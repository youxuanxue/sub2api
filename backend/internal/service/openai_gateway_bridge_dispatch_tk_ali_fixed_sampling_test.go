//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestIsNewAPIAliFixedSamplingModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"kimi-k3", true},
		{"Kimi-K3", true},
		{"kimi-k3-preview", true},
		{"kimi/kimi-k3", true},
		{"kimi-k2.5", false},
		{"kimi-k2.6", false},
		{"qwen-plus", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isNewAPIAliFixedSamplingModel(tc.model); got != tc.want {
			t.Fatalf("isNewAPIAliFixedSamplingModel(%q)=%v want %v", tc.model, got, tc.want)
		}
	}
}

func TestApplyNewAPIAliFixedSamplingShape_PinsTopP(t *testing.T) {
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":16}`)
	got := applyNewAPIAliFixedSamplingShape("kimi-k3", body)
	if !gjson.GetBytes(got, "top_p").Exists() {
		t.Fatal("expected top_p to be injected")
	}
	if gotTopP := gjson.GetBytes(got, "top_p").Float(); gotTopP != 0.95 {
		t.Fatalf("top_p=%v want 0.95", gotTopP)
	}
	if gjson.GetBytes(got, "temperature").Exists() {
		t.Fatal("temperature must stay omitted when client did not send it")
	}
}

func TestApplyNewAPIAliFixedSamplingShape_RewritesInvalidClientSampling(t *testing.T) {
	body := []byte(`{"model":"kimi-k3","temperature":0.7,"top_p":0.8,"n":2,"presence_penalty":0.5,"frequency_penalty":0.5,"messages":[{"role":"user","content":"hi"}]}`)
	got := applyNewAPIAliFixedSamplingShape("kimi-k3", body)
	if gjson.GetBytes(got, "top_p").Float() != 0.95 {
		t.Fatalf("top_p=%v want 0.95", gjson.GetBytes(got, "top_p").Float())
	}
	if gjson.GetBytes(got, "temperature").Float() != 1.0 {
		t.Fatalf("temperature=%v want 1.0", gjson.GetBytes(got, "temperature").Float())
	}
	if gjson.GetBytes(got, "n").Int() != 1 {
		t.Fatalf("n=%v want 1", gjson.GetBytes(got, "n").Int())
	}
	if gjson.GetBytes(got, "presence_penalty").Float() != 0 {
		t.Fatalf("presence_penalty=%v want 0", gjson.GetBytes(got, "presence_penalty").Float())
	}
	if gjson.GetBytes(got, "frequency_penalty").Float() != 0 {
		t.Fatalf("frequency_penalty=%v want 0", gjson.GetBytes(got, "frequency_penalty").Float())
	}
}

func TestApplyNewAPIAliFixedSamplingShape_LeavesOtherModels(t *testing.T) {
	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}]}`)
	got := applyNewAPIAliFixedSamplingShape("kimi-k2.5", body)
	if string(got) != string(body) {
		t.Fatalf("non-k3 body must be unchanged")
	}
}

func TestDispatchNewAPIAccountTestChatCompletions_PinsKimiK3TopP(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{Model: "kimi-k3"}, nil
	}

	account := &Account{
		ID:          110,
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
	c, _ := gin.CreateTestContext(nil)
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":1024}`)
	if err := dispatchNewAPIAccountTestChatCompletions(context.Background(), c, account, body); err != nil {
		t.Fatalf("dispatchNewAPIAccountTestChatCompletions: %v", err)
	}
	if topP := gjson.GetBytes(capturedBody, "top_p").Float(); topP != 0.95 {
		t.Fatalf("probe body top_p=%v want 0.95 (counter Ali adaptor 0.001 coercion)", topP)
	}
	if model := gjson.GetBytes(capturedBody, "model").String(); model != "kimi-k3" {
		t.Fatalf("model=%q want kimi-k3", model)
	}
}

func TestForwardAsChatCompletionsDispatched_PinsKimiK3TopP(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{Model: "kimi-k3"}, nil
	}

	account := &Account{
		ID:          110,
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
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`)
	if _, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", ""); err != nil {
		t.Fatalf("ForwardAsChatCompletionsDispatched: %v", err)
	}
	if topP := gjson.GetBytes(capturedBody, "top_p").Float(); topP != 0.95 {
		t.Fatalf("bridge body top_p=%v want 0.95", topP)
	}
	if temp := gjson.GetBytes(capturedBody, "temperature").Float(); temp != 1.0 {
		t.Fatalf("bridge body temperature=%v want 1.0", temp)
	}
	if !strings.Contains(string(capturedBody), `"model":"kimi-k3"`) {
		t.Fatalf("expected kimi-k3 in body, got %s", capturedBody)
	}
}

func TestAnthropicToChatBody_AppliesAliFixedSamplingForKimiK3(t *testing.T) {
	// ForwardAsAnthropicDispatched must call applyNewAPIAliFixedSamplingShape before
	// bridge.DispatchChatCompletions (literal call site is sentinel-pinned). Prove the
	// same composition the production path uses: convert then shape.
	temp := 0.7
	req := &apicompat.AnthropicRequest{
		Model:       "kimi-k3",
		MaxTokens:   16,
		Temperature: &temp,
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	chatBody, err := anthropicToChatCompletionsBody(req, "kimi-k3")
	if err != nil {
		t.Fatalf("anthropicToChatCompletionsBody: %v", err)
	}
	shaped := applyNewAPIAliFixedSamplingShape("kimi-k3", chatBody)
	if topP := gjson.GetBytes(shaped, "top_p").Float(); topP != 0.95 {
		t.Fatalf("shaped top_p=%v want 0.95", topP)
	}
	if gotTemp := gjson.GetBytes(shaped, "temperature").Float(); gotTemp != 1.0 {
		t.Fatalf("shaped temperature=%v want 1.0", gotTemp)
	}
}
