//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newKiroMirrorStubForSilentRefusalTest() *Account {
	return &Account{
		ID:          660,
		Name:        "kiro-us4",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":         "edge-relay-key",
			"base_url":        "https://api-us4.tokenkey.dev",
			"mirror_platform": PlatformKiro,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newKiroSilentRefusalRelayResponse() *http.Response {
	header := make(http.Header)
	header.Set(KiroOutcomeHeader, KiroSilentRefusalOutcome)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     header,
		Body: io.NopCloser(bytes.NewBufferString(
			`{"type":"error","error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`,
		)),
	}
}

func assertTerminalKiroSilentRefusal(t *testing.T, c *gin.Context, result *ForwardResult, err error) {
	t.Helper()
	require.Error(t, err)
	require.Nil(t, result)
	var silentRefusalErr *KiroSilentRefusalError
	require.ErrorAs(t, err, &silentRefusalErr)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "request-owned refusal must not trigger mirror failover")
	events := opsUpstreamErrorsForTest(c)
	require.Len(t, events, 1)
	require.Equal(t, PlatformKiro, events[0].Platform)
	require.Equal(t, "silent_refusal", events[0].Kind)
	require.Equal(t, "metering_without_output", events[0].Reason)
	require.Equal(t, http.StatusBadGateway, events[0].UpstreamStatusCode)
}

func TestGatewayService_Forward_KiroMirrorSilentRefusalStopsBeforeRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	upstream := &httpUpstreamRecorder{resp: newKiroSilentRefusalRelayResponse()}
	svc := &GatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
		httpUpstream: upstream,
	}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"policy-sensitive request"}],"stream":false}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroMirrorStubForSilentRefusalTest(), parsed)

	assertTerminalKiroSilentRefusal(t, c, result, err)
	require.Len(t, upstream.requests, 1, "terminal relay outcome must skip same-account retry")
	require.Empty(t, rec.Body.String())
}

func TestGatewayService_ForwardAsChatCompletions_KiroMirrorSilentRefusalStopsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	upstream := &httpUpstreamRecorder{resp: newKiroSilentRefusalRelayResponse()}
	svc := &GatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
		httpUpstream: upstream,
	}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"policy-sensitive request"}],"stream":false}`)

	result, err := svc.ForwardAsChatCompletions(
		context.Background(), c, newKiroMirrorStubForSilentRefusalTest(), body, nil,
	)

	assertTerminalKiroSilentRefusal(t, c, result, err)
	require.Len(t, upstream.requests, 1)
	require.Empty(t, rec.Body.String())
}
