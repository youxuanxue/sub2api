package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPlatformAPIKeyResponsesDropsCodexIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		baseURL      string
		userAgent    string
		passthrough  bool
		wantIdentity bool
	}{
		{
			name:         "official platform key drops codex identity",
			baseURL:      "https://api.openai.com",
			userAgent:    "codex_cli_rs/0.167.0 (Mac OS X 15.1.0; arm64) iTerm.app",
			wantIdentity: false,
		},
		{
			name:         "official platform key drops codex identity on passthrough",
			baseURL:      "https://api.openai.com",
			userAgent:    "codex_cli_rs/0.167.0 (Mac OS X 15.1.0; arm64) iTerm.app",
			passthrough:  true,
			wantIdentity: false,
		},
		{
			name:         "official platform key keeps a non-codex user agent",
			baseURL:      "https://api.openai.com",
			userAgent:    "python-requests/2.33.1",
			wantIdentity: false,
		},
		{
			name:         "custom base keeps codex identity",
			baseURL:      "https://relay.example",
			userAgent:    "codex_cli_rs/0.167.0",
			wantIdentity: true,
		},
		{
			name:         "custom base keeps codex identity on passthrough",
			baseURL:      "https://relay.example",
			userAgent:    "codex_cli_rs/0.167.0",
			passthrough:  true,
			wantIdentity: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.1-sol","stream":false,"input":"hi"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("User-Agent", tt.userAgent)
			c.Request.Header.Set("originator", "codex_cli_rs")
			c.Request.Header.Set("version", "0.167.0")
			c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
			c.Request.Header.Set("session_id", "session-keep")
			c.Request.Header.Set("x-codex-turn-state", "turn-state")

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"type":"invalid_request_error","message":"stop after capture"}}`))),
			}}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
				httpUpstream: upstream,
			}
			account := &Account{
				ID:          526,
				Name:        "gpt-api-or4-24",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":  "sk-test",
					"base_url": tt.baseURL,
				},
				Status:      StatusActive,
				Schedulable: true,
			}
			if tt.passthrough {
				account.Extra = map[string]any{"openai_passthrough": true}
			}

			_, err := svc.Forward(context.Background(), c, account, body)
			require.Error(t, err)
			require.NotNil(t, upstream.lastReq)
			headers := upstream.lastReq.Header
			if tt.wantIdentity {
				require.Equal(t, "codex_cli_rs", headers.Get("originator"))
				require.Equal(t, tt.userAgent, headers.Get("User-Agent"))
				require.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
				require.Equal(t, "turn-state", headers.Get("x-codex-turn-state"))
			} else {
				require.Empty(t, headers.Get("originator"))
				require.Empty(t, headers.Get("version"))
				require.Empty(t, headers.Get("OpenAI-Beta"))
				require.Empty(t, headers.Get("x-codex-turn-state"))
				if tt.userAgent == "python-requests/2.33.1" {
					require.Equal(t, tt.userAgent, headers.Get("User-Agent"))
				} else {
					require.Empty(t, headers.Get("User-Agent"))
				}
			}
			require.Equal(t, "session-keep", headers.Get("session_id"))
			require.Equal(t, "Bearer sk-test", headers.Get("Authorization"))
		})
	}
}

func TestStripPlatformOpenAIAPIKeyCodexIdentityLeavesOAuthUntouched(t *testing.T) {
	headers := make(http.Header)
	headers.Set("originator", "codex_cli_rs")
	headers.Set("User-Agent", "codex_cli_rs/0.167.0")
	stripPlatformOpenAIAPIKeyCodexIdentity(headers, &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	})
	require.Equal(t, "codex_cli_rs", headers.Get("originator"))
	require.Equal(t, "codex_cli_rs/0.167.0", headers.Get("User-Agent"))
}
