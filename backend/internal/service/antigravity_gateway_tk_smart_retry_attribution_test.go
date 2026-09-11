//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Prod + edge-us4-ls, 2026-09-10: Antigravity 5xx on gemini-2.5-flash-image
// (503, 4→22) and claude-opus-4-6 (502, 2→7) were booked as owner=platform /
// phase=internal, i.e. OUR bug, and became `code_owned` + confidence=high +
// repair_eligible in daily_error_report.py — queueing an automated code-repair
// Draft PR against code that is not at fault, while the real provider capacity
// signal never reached upstream_error_rate.
//
// Cause: handleSmartRetry's terminal exits emitted no ops upstream event, unlike
// every other exit from the Antigravity retry loop. The account-switch signal is
// converted to an UpstreamFailoverError before any handler sees an HTTP status,
// so hasOpsUpstreamErrorContext stayed false and classifyOpsErrorLog fell
// through to phase="internal" => owner="platform".
//
// These tests pin that each terminal exit records the provider verdict. The
// handler-side consequence (phase=upstream, owner=provider) is pinned in
// handler/ops_error_logger_tk_antigravity_smart_retry_test.go.
func antigravitySmartRetryOpsEvents(t *testing.T, c *gin.Context) []*OpsUpstreamErrorEvent {
	t.Helper()
	value, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return nil
	}
	events, ok := value.([]*OpsUpstreamErrorEvent)
	require.True(t, ok, "OpsUpstreamErrorsKey must hold []*OpsUpstreamErrorEvent")
	return events
}

func newAntigravitySmartRetryOpsContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

// TestSmartRetryLongDelaySwitchRecordsProviderVerdict covers the 429/503
// long-retryDelay exit that rate-limits the model and switches account.
func TestSmartRetryLongDelaySwitchRecordsProviderVerdict(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	c := newAntigravitySmartRetryOpsContext()
	account := &Account{ID: 7, Name: "acc-7", Type: AccountTypeOAuth, Platform: PlatformAntigravity}

	respBody := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"message": "Resource exhausted for model",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-2.5-flash-image"}, "reason": "RATE_LIMIT_EXCEEDED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "15s"}
			]
		}
	}`)
	header := http.Header{}
	header.Set("x-request-id", "req-abc")
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
	}

	params := antigravityRetryLoopParams{
		ctx:         context.Background(),
		prefix:      "[test]",
		account:     account,
		accessToken: "token",
		action:      "streamGenerateContent",
		body:        []byte(`{"input":"test"}`),
		accountRepo: repo,
		c:           c,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	}

	svc := &AntigravityGatewayService{retryWait: failOnAntigravityRetryWait(t)}
	result := svc.handleSmartRetry(params, resp, respBody, "https://ag-1.test", 0, []string{"https://ag-1.test"})

	require.NotNil(t, result)
	require.NotNil(t, result.switchError, "long delay must still switch account")

	events := antigravitySmartRetryOpsEvents(t, c)
	require.Len(t, events, 1, "the account-switch exit must leave an attributable upstream verdict")
	require.Equal(t, http.StatusServiceUnavailable, events[0].UpstreamStatusCode)
	require.Equal(t, PlatformAntigravity, events[0].Platform)
	require.Equal(t, account.ID, events[0].AccountID)
	require.Equal(t, "req-abc", events[0].UpstreamRequestID)
	require.Equal(t, "smart_retry_rate_limited", events[0].Kind)
}

// TestSmartRetryCapacityExhaustedRecordsProviderVerdict covers the
// MODEL_CAPACITY_EXHAUSTED exit, which returns the upstream response directly
// and deliberately does not switch account. This is the exact shape behind the
// gemini-2.5-flash-image 503 regression.
func TestSmartRetryCapacityExhaustedRecordsProviderVerdict(t *testing.T) {
	// Model capacity retries burn antigravityModelCapacityRetryMaxAttempts
	// attempts, all answered 503, then fall through to the terminal exit.
	exhausted := []byte(`{
		"error": {
			"code": 503,
			"status": "UNAVAILABLE",
			"message": "The model is overloaded. Please try again later.",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-2.5-flash-image"}, "reason": "MODEL_CAPACITY_EXHAUSTED"}
			]
		}
	}`)
	upstream := &mockSmartRetryUpstream{
		responses: []*http.Response{{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader(exhausted)),
		}},
		errors:     []error{nil},
		repeatLast: true,
	}

	repo := &stubAntigravityAccountRepo{}
	c := newAntigravitySmartRetryOpsContext()
	account := &Account{ID: 9, Name: "acc-9", Type: AccountTypeOAuth, Platform: PlatformAntigravity}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(exhausted)),
	}

	params := antigravityRetryLoopParams{
		ctx:          context.Background(),
		prefix:       "[test]",
		account:      account,
		accessToken:  "token",
		action:       "streamGenerateContent",
		body:         []byte(`{"input":"test"}`),
		accountRepo:  repo,
		httpUpstream: upstream,
		c:            c,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	}

	svc := &AntigravityGatewayService{retryWait: immediateAntigravityRetryWait}
	result := svc.handleSmartRetry(params, resp, exhausted, "https://ag-1.test", 0, []string{"https://ag-1.test"})

	require.NotNil(t, result)
	require.Equal(t, smartRetryActionBreakWithResp, result.action)

	events := antigravitySmartRetryOpsEvents(t, c)
	require.NotEmpty(t, events, "capacity exhaustion must be attributable to the provider")
	last := events[len(events)-1]
	require.Equal(t, http.StatusServiceUnavailable, last.UpstreamStatusCode)
	require.Equal(t, account.ID, last.AccountID)
	require.Contains(
		t,
		[]string{"smart_retry_capacity_exhausted", "smart_retry_capacity_dedup"},
		last.Kind,
	)
}

// TestSmartRetryAttributionIsNilSafe pins that attribution never panics on the
// non-gateway call paths that pass no gin context (account probes, tests).
func TestSmartRetryAttributionIsNilSafe(t *testing.T) {
	svc := &AntigravityGatewayService{}
	require.NotPanics(t, func() {
		svc.appendAntigravitySmartRetryOpsEvent(
			antigravityRetryLoopParams{account: &Account{ID: 1}},
			http.StatusServiceUnavailable, nil, nil, "smart_retry_exhausted",
		)
		svc.appendAntigravitySmartRetryOpsEvent(
			antigravityRetryLoopParams{c: newAntigravitySmartRetryOpsContext()},
			http.StatusServiceUnavailable, nil, nil, "smart_retry_exhausted",
		)
	})
}
