//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A nested TokenKey stub returning the unsupported-model SSOT 404 must be read
// as "model not served there": fail over without penalty, never cool the stub,
// never populate the platform-wide not-found cache.
func nestedUnsupportedModel404Body(t *testing.T, wire TkErrorWire, model string) []byte {
	t.Helper()
	body, err := json.Marshal(tkUnsupportedModelErrorFor(wire, model).Body())
	require.NoError(t, err)
	return body
}

func TestNestedGatewayUnsupportedModel404_NoModelCooldown(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformNewAPI, PlatformGemini, PlatformAntigravity} {
		repo := &anthropic404AccountRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{ID: 7, Platform: platform, Type: AccountTypeAPIKey}
		body := nestedUnsupportedModel404Body(t, TkErrorWireOpenAI, "does-not-exist")

		require.False(t, svc.HandleUpstreamModelNotFound(context.Background(), account, "does-not-exist", http.StatusNotFound, body), platform)
		require.False(t, svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, body, "does-not-exist"), platform)
		require.Zero(t, repo.modelRateLimitCalls, platform)
		require.Zero(t, repo.setErrorCalls, platform)
		require.Zero(t, repo.rateLimitCalls, platform)
	}
}

func TestNestedGatewayUnsupportedModel404_IsCapabilityFailoverNotPenalty(t *testing.T) {
	for _, wire := range []TkErrorWire{TkErrorWireOpenAI, TkErrorWireAnthropic, TkErrorWireGemini} {
		body := nestedUnsupportedModel404Body(t, wire, "m")
		require.True(t, isModelCapabilityFailure(http.StatusNotFound, body), wire)
	}
	// Legacy nested 400 keeps working during rollout.
	legacy := []byte(`{"error":{"type":"invalid_request_error","code":"unsupported_model","message":"Unsupported model: m"}}`)
	require.True(t, isModelCapabilityFailure(http.StatusBadRequest, legacy))
	// Arbitrary 404 is not a capability failure.
	require.False(t, isModelCapabilityFailure(http.StatusNotFound, []byte(`{"error":{"message":"Unknown URL"}}`)))
}

func TestNestedGatewayUnsupportedModel404_AnthropicFailsOverWithoutCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	cache := newTkModelNotFoundNegativeCache()
	svc := &GatewayService{tkModelNotFoundCache: cache}
	body := nestedUnsupportedModel404Body(t, TkErrorWireAnthropic, "claude-typo-9")
	resp := &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}
	account := &Account{ID: 12, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, "claude-typo-9")
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.False(t, cache.get(PlatformAnthropic, "claude-typo-9"))
}

func TestNestedGatewayUnsupportedModel404_OpenAICompatAccountFault(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &anthropic404AccountRepoStub{}}
	account := &Account{ID: 9, Platform: PlatformNewAPI, Type: AccountTypeAPIKey}
	body := nestedUnsupportedModel404Body(t, TkErrorWireOpenAI, "m")
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusNotFound, "Unsupported model: m", body))
}
