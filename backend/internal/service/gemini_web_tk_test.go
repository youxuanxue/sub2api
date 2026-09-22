package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiWebSelectedAccountHeaderBothEntryPoints(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, web := range []bool{true, false} {
			upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200,
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:   io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 28, Platform: PlatformGemini, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "worker-key"}}
			if web {
				account.Credentials["gemini_web"] = map[string]any{}
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			c.Request.Header.Set("X-TokenKey-Gemini-Web-Account-ID", "999")
			var err error
			if native {
				_, err = svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false,
					[]byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
			} else {
				_, err = svc.Forward(context.Background(), c, account,
					[]byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
			}
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			want := ""
			if web {
				want = "28"
			}
			require.Equal(t, want, upstream.lastReq.Header.Get("X-TokenKey-Gemini-Web-Account-ID"))
		}
	}
}
