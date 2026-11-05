//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// anthropicChatStickyUpstream records the Anthropic /v1/messages body that
// ForwardAsChatCompletions actually puts on the wire.
type anthropicChatStickyUpstream struct {
	body []byte
}

func (u *anthropicChatStickyUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, concurrency, nil)
}

func (u *anthropicChatStickyUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	if req != nil && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		u.body = append([]byte(nil), body...)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_anthropic_chat_sticky"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: error`,
			`data: {"type":"error","error":{"type":"api_error","message":"Insufficient balance"}}`,
			``,
		}, "\n"))),
	}, nil
}

func forwardAnthropicAPIKeyChat(t *testing.T, clientBody []byte, mode group.StickyRoutingMode) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(clientBody))
	c.Request.Header.Set("Content-Type", "application/json")
	// API key id + group mode are what resolveStickyStrategyFromGin / DeriveStickyKey read.
	c.Set("api_key", &APIKey{
		ID: 42,
		Group: &Group{
			ID:                7,
			StickyRoutingMode: string(mode),
		},
	})

	upstream := &anthropicChatStickyUpstream{}
	svc := &GatewayService{
		cfg:                 &config.Config{},
		httpUpstream:        upstream,
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	account := &Account{
		ID:          94,
		Name:        "anthropic-chat-sticky",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "test-key",
			"base_url": "https://cloudwise.example/api",
		},
	}
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, clientBody, nil)
	require.NotEmpty(t, upstream.body, "upstream request body was not sent: %v", err)
	return upstream.body
}

func TestForwardAsChatCompletions_AnthropicAPIKeySticky(t *testing.T) {
	const clientUserID = "client-session-keep"
	omitted := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"system","content":"sticky-system"},{"role":"user","content":"hi"}],"max_tokens":32,"stream":false}`)
	kept := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"system","content":"sticky-system"},{"role":"user","content":"hi"}],"max_tokens":32,"stream":false,"metadata":{"user_id":"client-session-keep"}}`)

	t.Run("injects when client omits metadata user id", func(t *testing.T) {
		got := forwardAnthropicAPIKeyChat(t, omitted, group.StickyRoutingModeAuto)
		uid := gjson.GetBytes(got, "metadata.user_id").String()
		require.NotEmpty(t, uid)
		require.Truef(t, strings.HasPrefix(uid, StickyDerivedKeyPrefix) && len(uid) > len(StickyDerivedKeyPrefix),
			"upstream metadata.user_id = %q", uid)
	})

	t.Run("strategy off does not invent a session", func(t *testing.T) {
		got := forwardAnthropicAPIKeyChat(t, omitted, group.StickyRoutingModeOff)
		require.False(t, gjson.GetBytes(got, "metadata.user_id").Exists(), "strategy off must not write metadata.user_id, body=%s", got)
	})

	t.Run("keeps client metadata user id", func(t *testing.T) {
		got := forwardAnthropicAPIKeyChat(t, kept, group.StickyRoutingModeAuto)
		require.Equal(t, clientUserID, gjson.GetBytes(got, "metadata.user_id").String(), "body=%s", got)
	})
}
