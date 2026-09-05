//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func forceCanonicalIngressStrict(t *testing.T, enabled bool) {
	t.Helper()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		canonicalIngressStrict: enabled,
		expiresAt:              time.Now().Add(time.Hour).UnixNano(),
	})
	t.Cleanup(func() {
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	})
}

// TestForwardCountTokens_EstimateSkipsStrictUAGate pins the control-flow order
// companion extraction must preserve: shouldEstimateCountTokensLocally
// short-circuits BEFORE the canonical-OAuth UA gate. Estimate platforms
// (Antigravity/Gemini/Kiro) must keep returning local estimates even when
// strict ingress would reject the UA on Anthropic OAuth paths.
func TestForwardCountTokens_EstimateSkipsStrictUAGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forceCanonicalIngressStrict(t, true)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "openai-python/1.0") // rejected by strict allow-list
	c.Request = req

	svc := &GatewayService{settingService: &SettingService{}}
	account := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-6",
		Body:  mustNewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)),
	}

	err := svc.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, "estimate path must not hit UA gate; got body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), `"input_tokens"`)
}

// TestTkPrepareCountTokensAnthropicBody_DoesNotEnforceUAGate pins that the
// pre-estimate companion never writes a UA-gate 403 (gate stays in
// ForwardCountTokens after shouldEstimateCountTokensLocally).
func TestTkPrepareCountTokensAnthropicBody_DoesNotEnforceUAGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forceCanonicalIngressStrict(t, true)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "openai-python/1.0")
	c.Request = req

	svc := &GatewayService{settingService: &SettingService{}}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	stored := append([]byte(nil), body...)
	replaceBody := func(next []byte) error {
		stored = append([]byte(nil), next...)
		return nil
	}
	getBody := func() []byte { return stored }

	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	next, model, err := svc.tkPrepareCountTokensAnthropicBody(
		context.Background(), c, account, body, "claude-sonnet-4-5", replaceBody, getBody,
	)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", model)
	require.NotEmpty(t, next)
	require.NotEqual(t, http.StatusForbidden, rec.Code,
		"prepare companion must not enforce UA gate; got body=%s", rec.Body.String())
}
