package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestGatewayServiceForward_ClientCanceledTransportDoesNotWrite502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{name: "canonical"},
		{name: "api-key passthrough", extra: map[string]any{"anthropic_passthrough": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")

			svc := &GatewayService{
				cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
					Enabled: false,
				}}},
				httpUpstream: &httpUpstreamRecorder{err: context.Canceled},
			}
			account := &Account{
				ID:          901,
				Name:        "anthropic-client-cancel",
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.anthropic.com"},
				Extra:       tc.extra,
				Status:      StatusActive,
				Schedulable: true,
			}
			parsed := &ParsedRequest{
				Body:   NewRequestBodyRef(body),
				Model:  "claude-opus-4-8",
				Stream: false,
			}

			result, err := svc.Forward(ctx, c, account, parsed)

			require.Nil(t, result)
			require.ErrorIs(t, err, context.Canceled)
			require.False(t, c.Writer.Written(), "service must leave client-cancel status finalization to the handler")
			require.Empty(t, rec.Body.String())
			events, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			require.NotEmpty(t, events)
		})
	}
}
