package service

import (
	"net/http"
	"testing"
)

func TestIsUpstreamModelNotFoundError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{
			name:       "404 model not found message",
			statusCode: http.StatusNotFound,
			body:       []byte(`{"error":{"message":"model not found"}}`),
			want:       true,
		},
		{
			name:       "404 model_not_found code",
			statusCode: http.StatusNotFound,
			body:       []byte(`{"error":{"code":"model_not_found","message":"The requested model was not found"}}`),
			want:       true,
		},
		{
			name:       "404 unknown model message",
			statusCode: http.StatusNotFound,
			body:       []byte(`{"error":{"message":"unknown model gpt-5.4"}}`),
			want:       true,
		},
		{
			name:       "404 endpoint not found is not model specific",
			statusCode: http.StatusNotFound,
			body:       []byte(`{"error":{"message":"endpoint not found"}}`),
			want:       false,
		},
		{
			name:       "404 arbitrary body is not model specific",
			statusCode: http.StatusNotFound,
			body:       []byte(`404 page not found`),
			want:       false,
		},
		{
			name:       "non 404 does not match",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"model not found"}}`),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUpstreamModelNotFoundError(tt.statusCode, tt.body); got != tt.want {
				t.Fatalf("isUpstreamModelNotFoundError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAntigravityModelNotFoundKeepsBare404Fallback(t *testing.T) {
	if !isModelNotFoundError(http.StatusNotFound, []byte(`endpoint not found`)) {
		t.Fatal("antigravity model-not-found helper should keep bare 404 fallback")
	}
}

func TestIsOpenAICodexPlanGatedModelError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{
			name:       "400 codex plan gated detail payload",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`),
			want:       true,
		},
		{
			name:       "400 codex plan gated error message payload",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."}}`),
			want:       true,
		},
		{
			name:       "400 unrelated invalid request does not match",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"Invalid schema for response_format 'agentic_plan'"}}`),
			want:       false,
		},
		{
			name:       "404 with plan gated message does not match",
			statusCode: http.StatusNotFound,
			body:       []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`),
			want:       false,
		},
		{
			name:       "400 empty body does not match",
			statusCode: http.StatusBadRequest,
			body:       nil,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOpenAICodexPlanGatedModelError(tt.statusCode, tt.body); got != tt.want {
				t.Fatalf("isOpenAICodexPlanGatedModelError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsOpenAICompatibleModelNotFound400(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "structured code", body: `{"error":{"code":"model_not_found","message":"No such deployment"}}`, want: true},
		{name: "unknown provider", body: `{"error":{"message":"Unknown provider for model claude-x"}}`, want: true},
		{name: "model not found", body: `{"error":{"message":"Model not found: claude-x"}}`, want: true},
		{name: "model unsupported", body: `{"error":{"message":"The requested model is not supported"}}`, want: true},
		{name: "plain text compatible gateway", body: `unknown provider for model claude-x`, want: true},
		{name: "invalid parameter remains terminal", body: `{"error":{"code":"invalid_request_error","message":"Invalid value for temperature"}}`, want: false},
		{name: "structured non-matching code overrides model not found message", body: `{"error":{"code":"invalid_request_error","message":"Model not found: claude-x"}}`, want: false},
		{name: "structured non-matching error code overrides unsupported message", body: `{"error":{"code":"upstream_error","message":"The requested model is not supported"}}`, want: false},
		{name: "echoed phrase is ignored", body: `{"error":{"message":"Invalid request"},"echo":"model not found"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOpenAICompatibleModelNotFound400([]byte(tt.body)); got != tt.want {
				t.Fatalf("isOpenAICompatibleModelNotFound400() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsUpstreamModelRetiredError(t *testing.T) {
	nvidiaBuild410 := `{"error":{"code":"bad_response_status_code","message":"The model 'deepseek-ai/deepseek-v4-pro-0813' has reached its end of life on 2026-09-14T08:00:00Z and is no longer available.","param":"","type":"bad_response_status_code"}}`

	tests := []struct {
		name       string
		statusCode int
		body       string
		msg        string
		want       bool
	}{
		{
			name:       "nvidia build 410 EOL body",
			statusCode: http.StatusGone,
			body:       nvidiaBuild410,
			want:       true,
		},
		{
			name:       "bare 410 does not prove model retirement",
			statusCode: http.StatusGone,
			body:       "",
			want:       false,
		},
		{
			name:       "400 with reached its end of life phrase",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"The model gpt-legacy has reached its end of life and is no longer available."}}`,
			want:       true,
		},
		{
			name:       "400 with model has been retired phrase",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"This model has been retired. Please use v2."}}`,
			want:       true,
		},
		{
			name:       "message parameter carries retirement phrase",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"code":"bad_response_status_code"}}`,
			msg:        "The model deepseek-v4-pro has reached its end of life",
			want:       true,
		},
		{
			name:       "generic 400 parameter error is not retired",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"code":"invalid_request_error","message":"Invalid value for temperature"}}`,
			want:       false,
		},
		{
			name:       "generic 500 is not retired",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"message":"Internal server error"}}`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUpstreamModelRetiredError(tt.statusCode, []byte(tt.body), tt.msg); got != tt.want {
				t.Fatalf("isUpstreamModelRetiredError() = %v, want %v", got, tt.want)
			}
			if got := IsUpstreamModelRetiredError(tt.statusCode, []byte(tt.body), tt.msg); got != tt.want {
				t.Fatalf("IsUpstreamModelRetiredError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelRetirementRequiresModelDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"expired file", 410, `{"error":{"message":"The uploaded file is no longer available"}}`},
		{"expired session", 400, `{"error":{"message":"The session has reached its end of life"}}`},
		{"echoed prompt", 400, `{"error":{"message":"Invalid parameter"},"request":{"model":"current","input":"This model has been retired"}}`},
		{"transient model capacity", 503, `{"error":{"message":"This model is temporarily no longer available"}}`},
		{"credential expiration", 401, `{"error":{"message":"The API key for this model has reached its end of life"}}`},
		{"success payload", 200, `{"message":"This model has been retired"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if isUpstreamModelRetiredError(tc.status, []byte(tc.body)) {
				t.Fatal("non-retirement must not cool the model")
			}
		})
	}
}
