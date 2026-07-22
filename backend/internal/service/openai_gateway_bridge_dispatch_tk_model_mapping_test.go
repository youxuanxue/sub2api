//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestRewriteNewAPIBridgeBodyModel_GLMDatedAlias(t *testing.T) {
	account := &Account{
		Platform: PlatformNewAPI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"glm-4.7": "glm-4.7",
			},
		},
	}
	body := []byte(`{"model":"glm-4-7-251222","messages":[{"role":"user","content":"hi"}]}`)
	got := rewriteNewAPIBridgeBodyModel(account, body, "")
	if model := gjson.GetBytes(got, "model").String(); model != "glm-4.7" {
		t.Fatalf("rewriteNewAPIBridgeBodyModel model = %q, want glm-4.7", model)
	}
}

func TestResolveOpenAIForwardModel_GLMDatedVolcengineAlias(t *testing.T) {
	account := &Account{
		Platform: PlatformNewAPI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"glm-4.7": "glm-4.7",
			},
		},
	}
	if got := resolveOpenAIForwardModel(account, "glm-4-7-251222", ""); got != "glm-4.7" {
		t.Fatalf("resolveOpenAIForwardModel(glm-4-7-251222) = %q, want glm-4.7", got)
	}
}

func TestForwardAsChatCompletionsDispatched_RewritesGLMDatedModelBeforeBridge(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, in bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		if strings.TrimSpace(in.APIKey) == "" {
			t.Fatal("expected bridge api key")
		}
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{
			Model: "glm-4.7",
		}, nil
	}

	account := &Account{
		ID:          60,
		Platform:    PlatformNewAPI,
		ChannelType: 17,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
			"model_mapping": map[string]any{
				"glm-4.7": "glm-4.7",
			},
		},
	}
	svc := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(nil)
	body := []byte(`{"model":"glm-4-7-251222","messages":[{"role":"user","content":"hi"}]}`)

	_, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	if err != nil {
		t.Fatalf("ForwardAsChatCompletionsDispatched: %v", err)
	}
	if model := gjson.GetBytes(capturedBody, "model").String(); model != "glm-4.7" {
		t.Fatalf("bridge body model = %q, want glm-4.7", model)
	}
}

func TestForwardAsChatCompletionsDispatched_VertexLocationUsesMappedModel(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedInput bridge.ChannelContextInput
	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, in bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedInput = in
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{Model: "gemini-3.6-flash"}, nil
	}

	account := &Account{
		ID:          47,
		Platform:    PlatformNewAPI,
		ChannelType: 41,
		Type:        AccountTypeServiceAccount,
		Credentials: map[string]any{
			"service_account_json": `{"type":"service_account","project_id":"tk-proj","client_email":"x@tk-proj.iam.gserviceaccount.com","private_key":"test"}`,
			"location":             "us-central1",
			"model_mapping": map[string]any{
				"gemini-next": "gemini-3.6-flash",
			},
		},
	}
	svc := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(nil)
	body := []byte(`{"model":"gemini-next","messages":[{"role":"user","content":"hi"}]}`)

	_, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	if err != nil {
		t.Fatalf("ForwardAsChatCompletionsDispatched: %v", err)
	}
	if model := gjson.GetBytes(capturedBody, "model").String(); model != "gemini-3.6-flash" {
		t.Fatalf("bridge body model = %q, want gemini-3.6-flash", model)
	}
	if capturedInput.VertexLocation != vertexGlobalLocation {
		t.Fatalf("bridge VertexLocation = %q, want %q", capturedInput.VertexLocation, vertexGlobalLocation)
	}
}

func TestForwardAsChatCompletionsDispatched_QwenNonStreamingDisablesThinking(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{Model: "qwen3-8b"}, nil
	}

	account := &Account{
		ID:          60,
		Platform:    PlatformNewAPI,
		ChannelType: 17,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"},
	}
	svc := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(nil)
	body := []byte(`{"model":"qwen3-8b","stream":false,"enable_thinking":true,"messages":[{"role":"user","content":"hi"}]}`)

	_, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	if err != nil {
		t.Fatalf("ForwardAsChatCompletionsDispatched: %v", err)
	}
	if got := gjson.GetBytes(capturedBody, "enable_thinking"); !got.Exists() || got.Bool() {
		t.Fatalf("bridge body enable_thinking = %s, want false", got.Raw)
	}
}
