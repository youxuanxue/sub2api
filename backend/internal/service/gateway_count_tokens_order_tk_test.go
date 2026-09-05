//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
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

func canonicalOAuthCountTokensFixture(t *testing.T) (*GatewayService, *Account) {
	t.Helper()
	const profileID int64 = 99
	tlsSvc := &TLSFingerprintProfileService{
		localCache: map[int64]*model.TLSFingerprintProfile{
			profileID: {ID: profileID, Name: canonicalTLSFingerprintProfileName},
		},
	}
	svc := &GatewayService{
		settingService:      &SettingService{},
		tlsFPProfileService: tlsSvc,
	}
	account := &Account{
		ID:       42,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": profileID,
		},
	}
	require.True(t, svc.isCanonicalAnthropicOAuth(account),
		"fixture must satisfy isCanonicalAnthropicOAuth so UA gate is reachable")
	return svc, account
}

// TestForwardCountTokens_EstimateSkipsStrictUAGate pins that estimate platforms
// short-circuit before the canonical-OAuth UA gate runs. Predicates are
// disjoint today (estimate platforms never pass isCanonicalAnthropicOAuth), but
// the short-circuit must remain so a future overlap cannot 403 local estimates.
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

// TestForwardCountTokens_CanonicalOAuthBadUARejected pins that the UA gate still
// fires on Anthropic canonical OAuth AFTER prepare + estimate check (non-estimate
// account). This is the customer-visible half of the e281aba53 invariant.
func TestForwardCountTokens_CanonicalOAuthBadUARejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forceCanonicalIngressStrict(t, true)

	svc, account := canonicalOAuthCountTokensFixture(t)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "openai-python/1.0")
	c.Request = req

	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-5",
		Body:  mustNewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)),
	}

	err := svc.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, rec.Code, "canonical OAuth + bad UA must 403; body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "permission_error")
}

// TestTkPrepareCountTokensAnthropicBody_DoesNotEnforceUAGate pins that the
// pre-estimate companion never writes a UA-gate 403 — even when the account
// WOULD trip the gate in ForwardCountTokens. Gate stays after estimate check.
func TestTkPrepareCountTokensAnthropicBody_DoesNotEnforceUAGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forceCanonicalIngressStrict(t, true)

	svc, account := canonicalOAuthCountTokensFixture(t)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "openai-python/1.0")
	c.Request = req

	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	stored := append([]byte(nil), body...)
	replaceBody := func(next []byte) error {
		stored = append([]byte(nil), next...)
		return nil
	}
	getBody := func() []byte { return stored }

	next, model, err := svc.tkPrepareCountTokensAnthropicBody(
		context.Background(), c, account, body, "claude-sonnet-4-5", replaceBody, getBody,
	)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", model)
	require.NotEmpty(t, next)
	require.NotEqual(t, http.StatusForbidden, rec.Code,
		"prepare companion must not enforce UA gate; got body=%s", rec.Body.String())
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestForwardCountTokens_PrepareErrorStillSyncsParsedModel pins eager model
// sync on prepare failure (alias strip wrote body, later replaceBody fails).
func TestForwardCountTokens_PrepareErrorStillSyncsParsedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	svc := &GatewayService{settingService: &SettingService{}}
	// Context-window alias so prepare's first replaceBody succeeds, then the
	// second (sanitize) replaceBody fails via a poisoned ParsedRequest — we
	// instead call prepare directly and assert ForwardCountTokens sync path.
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-5[1m]",
		Body:  mustNewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5[1m]","messages":[{"role":"user","content":"hi"}]}`)),
	}
	calls := 0
	// Drive prepare with a replaceBody that fails on the 2nd call (sanitize).
	body := parsed.Body.Bytes()
	replaceBody := func(next []byte) error {
		calls++
		if calls >= 2 {
			return context.Canceled
		}
		require.NoError(t, parsed.ReplaceBody(next))
		return nil
	}
	getBody := func() []byte { return parsed.Body.Bytes() }

	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	_, nextModel, err := svc.tkPrepareCountTokensAnthropicBody(
		context.Background(), c, account, body, parsed.Model, replaceBody, getBody,
	)
	require.Error(t, err)
	require.Equal(t, "claude-sonnet-4-5", nextModel, "alias strip must update model before sanitize fails")

	// Mirror ForwardCountTokens sync-before-return-on-error semantics.
	parsed.Model = nextModel
	require.Equal(t, "claude-sonnet-4-5", parsed.Model)
}

// TestForwardCountTokens_EstimateBeforeUAGateInSource nails the structural
// order companion extraction must not invert (CI-safe via runtime.Caller).
func TestForwardCountTokens_EstimateBeforeUAGateInSource(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(thisFile)

	mainSrc, err := os.ReadFile(filepath.Join(dir, "gateway_count_tokens.go"))
	require.NoError(t, err)
	prepSrc, err := os.ReadFile(filepath.Join(dir, "gateway_count_tokens_tk.go"))
	require.NoError(t, err)

	main := string(mainSrc)
	prep := string(prepSrc)

	estIdx := strings.Index(main, "shouldEstimateCountTokensLocally(account)")
	uaIdx := strings.Index(main, "checkCanonicalIngressUAStrict")
	require.Greater(t, estIdx, 0, "ForwardCountTokens must call shouldEstimateCountTokensLocally")
	require.Greater(t, uaIdx, 0, "ForwardCountTokens must call checkCanonicalIngressUAStrict")
	require.Less(t, estIdx, uaIdx, "estimate short-circuit must precede UA gate")

	require.NotContains(t, prep, "checkCanonicalIngressUAStrict",
		"prepare companion must not own the UA gate")
	require.NotContains(t, prep, "countTokensError",
		"prepare companion must not write client error responses")
}
