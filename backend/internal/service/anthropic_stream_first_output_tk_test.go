//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStreamFirstOutputFailoverErrorAfterKeepaliveFlag(t *testing.T) {
	err := streamFirstOutputFailoverErrorAfterKeepalive()
	var fo *UpstreamFailoverError
	require.ErrorAs(t, err, &fo)
	require.True(t, fo.SafeToFailoverAfterWrite)
	require.Equal(t, GatewayFailureReason("first_output_unavailable"), fo.Reason)
}

func TestAnthropicPassthroughStreamFirstOutputBudgetBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize:             defaultMaxLineSize,
				StreamKeepaliveInterval: 0,
			},
		},
		rateLimitService: &RateLimitService{},
	}

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}

	parent := context.Background()
	streamCtx, guard := armStreamFirstOutputGuard(parent, 25*time.Millisecond)

	done := make(chan struct{})
	var gotErr error
	go func() {
		defer close(done)
		_, gotErr = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(
			parent, resp, c, &Account{ID: 1}, time.Now(), "claude-opus-5", guard, streamCtx,
		)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after first-output budget")
	}

	var fo *UpstreamFailoverError
	require.ErrorAs(t, gotErr, &fo)
	require.Equal(t, GatewayFailureReason("first_output_unavailable"), fo.Reason)
	require.False(t, fo.SafeToFailoverAfterWrite, "no client bytes yet → silent failover")
	require.Empty(t, rec.Body.String())
}

func TestAnthropicPassthroughStreamFirstOutputCommitsOnMessageStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
		},
		rateLimitService: &RateLimitService{},
	}

	body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	parent := context.Background()
	streamCtx, guard := armStreamFirstOutputGuard(parent, time.Second)

	result, err := svc.handleStreamingResponseAnthropicAPIKeyPassthrough(
		parent, resp, c, &Account{ID: 1}, time.Now(), "claude-opus-5", guard, streamCtx,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, guard.Committed(), "message_start data frame must commit the budget")
	require.False(t, guard.BudgetFired(streamCtx, parent))
	require.Contains(t, rec.Body.String(), "message_start")
}
