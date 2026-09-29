//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiWebSelectedAccountHeaderBothEntryPoints(t *testing.T) {
	type entry struct {
		name string
		run  func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error
	}
	entries := []entry{
		{"native", func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
			_, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false,
				[]byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
			return err
		}},
		{"messages", func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
			_, err := svc.Forward(context.Background(), c, account,
				[]byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
			return err
		}},
		{"chat", func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
			_, err := svc.ForwardAsChatCompletions(context.Background(), c, account,
				[]byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`))
			return err
		}},
	}
	for _, ep := range entries {
		for _, web := range []bool{true, false} {
			t.Run(ep.name+"_web_"+strconv.FormatBool(web), func(t *testing.T) {
				upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200,
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))}}
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
				account := &Account{ID: 28, Platform: PlatformGemini, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "worker-key"}}
				if web {
					account.Credentials["gemini_web"] = map[string]any{"runtime": map[string]any{"version": 1}}
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
				c.Request.Header.Set("X-TokenKey-Gemini-Web-Account-ID", "999")
				require.NoError(t, ep.run(svc, c, account))
				require.NotNil(t, upstream.lastReq)
				want := ""
				if web {
					want = "28"
				}
				require.Equal(t, want, upstream.lastReq.Header.Get("X-TokenKey-Gemini-Web-Account-ID"))
			})
		}
	}
}

func TestTkIsGeminiWebMissingAccountReference401(t *testing.T) {
	web := &Account{ID: 28, Platform: PlatformGemini, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "k", "gemini_web": map[string]any{"runtime": map[string]any{"version": 1}}}}
	plain := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "k"}}
	body := []byte(`{"error":{"code":401,"message":"Missing Gemini Web account reference"}}`)
	require.True(t, tkIsGeminiWebMissingAccountReference401(web, 401, "Missing Gemini Web account reference", nil))
	require.True(t, tkIsGeminiWebMissingAccountReference401(web, 401, "", body))
	require.False(t, tkIsGeminiWebMissingAccountReference401(web, 400, "Missing Gemini Web account reference", nil),
		"400 is already a client fault path; skip helper is for legacy 401 only")
	require.False(t, tkIsGeminiWebMissingAccountReference401(web, 401, "Invalid worker API key", nil))
	require.False(t, tkIsGeminiWebMissingAccountReference401(plain, 401, "Missing Gemini Web account reference", nil))

	require.True(t, geminiWebMissingAccountReferenceClientFault(web, 401, body))
	require.True(t, geminiWebMissingAccountReferenceClientFault(web, 400, body))
	require.False(t, geminiWebMissingAccountReferenceClientFault(plain, 401, body))
	require.False(t, geminiWebMissingAccountReferenceClientFault(web, 403, body))
}

func TestRateLimitService_HandleUpstreamError_GeminiWebMissingAccountReference401_DoesNotSetError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       28,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":    "worker-key",
			"gemini_web": map[string]any{"runtime": map[string]any{"version": 1}},
		},
	}
	body := []byte(`{"error":{"code":401,"status":"UNAUTHENTICATED","message":"Missing Gemini Web account reference"}}`)
	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)
	require.False(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)

	// Genuine worker key rejection still disables API-key accounts.
	badKey := []byte(`{"error":{"code":401,"message":"Invalid worker API key"}}`)
	shouldDisable = service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, badKey)
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
}
