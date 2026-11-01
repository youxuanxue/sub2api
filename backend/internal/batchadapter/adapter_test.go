package batchadapter

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
)

type managedExecutionContextTestKey struct{}

type fakeRunner struct {
	request  ProviderRequest
	response ProviderResponse
	err      error
}

func (r *fakeRunner) Run(_ context.Context, request ProviderRequest) (ProviderResponse, error) {
	r.request = request
	return r.response, r.err
}

func requestLine(endpoint string) batchcontract.RequestLine {
	return batchcontract.RequestLine{CustomID: "row-1", Method: http.MethodPost, URL: endpoint, Body: []byte(`{"model":"fixture-model","messages":[{"role":"user","content":"hello"}]}`)}
}

func TestProviderCapabilities(t *testing.T) {
	openai := NewOpenAIAdapter(nil).Capability()
	if !openai.Managed || openai.NativeBatch || !openai.SupportsEndpoint(batchcontract.EndpointChatCompletions) || !openai.SupportsEndpoint(batchcontract.EndpointResponses) {
		t.Fatalf("OpenAI capability = %+v", openai)
	}
	antigravity := NewAntigravityAdapter(nil).Capability()
	if !antigravity.Managed || antigravity.NativeBatch {
		t.Fatalf("Antigravity capability = %+v", antigravity)
	}
	kiro := NewKiroAdapter(nil).Capability()
	if kiro.Managed || kiro.NativeBatch {
		t.Fatalf("Kiro must remain disabled by default: %+v", kiro)
	}
}

func TestValidateRequestChecksOpenAIEndpoints(t *testing.T) {
	adapter := NewOpenAIAdapter(nil)
	for _, endpoint := range []string{batchcontract.EndpointChatCompletions, batchcontract.EndpointResponses} {
		if err := adapter.ValidateRequest(requestLine(endpoint), endpoint); err != nil {
			t.Fatalf("ValidateRequest(%s): %v", endpoint, err)
		}
	}
	if err := adapter.ValidateRequest(requestLine("/v1/embeddings"), "/v1/embeddings"); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("unsupported endpoint error = %v", err)
	}
	line := requestLine(batchcontract.EndpointResponses)
	line.Method = http.MethodGet
	if err := adapter.ValidateRequest(line, batchcontract.EndpointResponses); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("method error = %v", err)
	}
}

func TestManagedExecuteForwardsToRunnerAndNormalizesUsage(t *testing.T) {
	tier := "default"
	runner := &fakeRunner{response: ProviderResponse{
		StatusCode: http.StatusOK, RequestID: "req_fixture",
		Body:  []byte(`{"id":"resp_fixture","usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":2}}}`),
		Model: "fixture-model", UpstreamModel: "mapped-model", BillingModel: "priced-model", ServiceTier: &tier,
	}}
	adapter := NewOpenAIAdapter(runner)
	got, err := adapter.Execute(context.Background(), ManagedRequest{AccountID: 42, Endpoint: batchcontract.EndpointChatCompletions, Method: http.MethodPost, Model: "fixture-model", Body: requestLine(batchcontract.EndpointChatCompletions).Body})
	if err != nil {
		t.Fatal(err)
	}
	if runner.request.AccountID != 42 || runner.request.Endpoint != batchcontract.EndpointChatCompletions || runner.request.Model != "fixture-model" {
		t.Fatalf("runner request = %+v", runner.request)
	}
	if got.Response.StatusCode != http.StatusOK || got.Response.RequestID != "req_fixture" || got.Usage != (Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10, CacheReadInputTokens: 2}) || got.Error != nil {
		t.Fatalf("managed response = %+v", got)
	}
	if got.Model != "fixture-model" || got.UpstreamModel != "mapped-model" || got.BillingModel != "priced-model" || got.ServiceTier == nil || *got.ServiceTier != tier {
		t.Fatalf("managed billing metadata = %+v", got)
	}
}

func TestManagedExecutePreservesSchedulerExecutionContext(t *testing.T) {
	runner := &fakeRunner{response: ProviderResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{}}`)}}
	adapter := NewOpenAIAdapter(runner)
	marker := context.WithValue(context.Background(), managedExecutionContextTestKey{}, "scheduler-plan")
	_, err := adapter.Execute(context.Background(), ManagedRequest{
		ExecutionContext: marker,
		AccountID:        42,
		Endpoint:         batchcontract.EndpointResponses,
		Method:           http.MethodPost,
		Model:            "fixture-model",
		Body:             requestLine(batchcontract.EndpointResponses).Body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.request.ExecutionContext != marker {
		t.Fatalf("runner lost scheduler execution context")
	}
}

func TestManagedExecuteClassifiesProviderErrors(t *testing.T) {
	tests := []struct {
		status int
		class  ErrorClass
	}{
		{http.StatusTooManyRequests, ErrorRetryable},
		{http.StatusBadGateway, ErrorRetryable},
		{http.StatusBadRequest, ErrorPermanent},
	}
	for _, tt := range tests {
		runner := &fakeRunner{response: ProviderResponse{StatusCode: tt.status, Body: []byte(`{"error":{"type":"fixture_error","message":"fixture failure"}}`)}}
		got, err := NewOpenAIAdapter(runner).Execute(context.Background(), ManagedRequest{AccountID: 1, Endpoint: batchcontract.EndpointResponses, Method: http.MethodPost, Body: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if got.Error == nil || got.Error.Class != tt.class || got.Error.StatusCode != tt.status {
			t.Fatalf("status %d response = %+v", tt.status, got)
		}
	}
}

func TestAntigravityAndKiroUsageFixtures(t *testing.T) {
	antigravityRunner := &fakeRunner{response: ProviderResponse{StatusCode: http.StatusOK, Body: []byte(`{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"totalTokenCount":16}}`)}}
	got, err := NewAntigravityAdapter(antigravityRunner).Execute(context.Background(), ManagedRequest{AccountID: 1, Endpoint: batchcontract.EndpointResponses, Method: http.MethodPost, Body: []byte(`{}`)})
	if err != nil || got.Usage != (Usage{InputTokens: 11, OutputTokens: 5, TotalTokens: 16}) {
		t.Fatalf("Antigravity fixture = %+v, err=%v", got, err)
	}
	kiro, err := NewKiroAdapter(&fakeRunner{}).Execute(context.Background(), ManagedRequest{AccountID: 1, Endpoint: batchcontract.EndpointResponses, Method: http.MethodPost, Body: []byte(`{}`)})
	if !errors.Is(err, ErrManagedDisabled) || kiro.Response.StatusCode != 0 || kiro.Usage != (Usage{}) || kiro.Error != nil {
		t.Fatalf("Kiro execution = %+v, err=%v", kiro, err)
	}
}

func TestUnsupportedNativeAdapterIsExplicit(t *testing.T) {
	capability := NewOpenAIAdapter(nil).Capability()
	native := NewUnsupportedNativeAdapter(capability)
	if _, err := native.Submit(context.Background(), NativeSubmitRequest{}); !errors.Is(err, ErrNativeUnsupported) {
		t.Fatalf("native submit err = %v", err)
	}
}
