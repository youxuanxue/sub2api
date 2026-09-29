//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 三条传输层失败处理的共同契约：请求 context 已取消（客户端断开）时不记 Ops 上游错误事件；
// 请求 context 仍存活时即便错误是 context.Canceled 也照常记录，不归为客户端断开。
func TestTransportErrorHandlers_ClientDisconnectRecordsNoOpsEvent(t *testing.T) {
	account := &Account{ID: 9, Name: "acc", Platform: PlatformAnthropic}
	clientErr := &url.Error{Op: "Post", URL: "https://upstream.example/v1/messages", Err: context.Canceled}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	handlers := []struct {
		name   string
		handle func(ctx context.Context, c *gin.Context) error
	}{
		{"anthropic", func(ctx context.Context, c *gin.Context) error {
			s := &GatewayService{accountRepo: &transportTempUnschedRepoStub{}}
			return s.handleUpstreamTransportError(ctx, c, account, clientErr, OpsUpstreamErrorEvent{})
		}},
		{"gemini", func(ctx context.Context, c *gin.Context) error {
			s := &GeminiMessagesCompatService{accountRepo: &transportTempUnschedRepoStub{}}
			return s.handleUpstreamTransportError(ctx, c, account, clientErr)
		}},
		{"openai", func(ctx context.Context, c *gin.Context) error {
			s := &OpenAIGatewayService{accountRepo: &openaiTransportAccountRepoStub{}}
			return s.handleOpenAIUpstreamTransportError(ctx, c, account, clientErr, false)
		}},
	}

	for _, h := range handlers {
		t.Run(h.name+"/client disconnected", func(t *testing.T) {
			c := newTransportErrorTestGin(t)

			err := h.handle(canceledCtx, c)

			require.ErrorIs(t, err, context.Canceled)
			_, recorded := c.Get(OpsUpstreamErrorsKey)
			require.False(t, recorded, "客户端断开不是上游故障")
			_, messageSet := c.Get(OpsUpstreamErrorMessageKey)
			require.False(t, messageSet)
		})
		t.Run(h.name+"/request context alive", func(t *testing.T) {
			c := newTransportErrorTestGin(t)

			err := h.handle(context.Background(), c)

			require.ErrorIs(t, err, context.Canceled)
			raw, recorded := c.Get(OpsUpstreamErrorsKey)
			require.True(t, recorded)
			require.Len(t, raw.([]*OpsUpstreamErrorEvent), 1)
		})
	}
}

func TestAntigravityCompatTransportError_ClientDisconnectWrites499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	err := (&AntigravityGatewayService{}).handleAntigravityCompatTransportError(c, context.Canceled)

	require.Error(t, err)
	require.Equal(t, antigravityStatusClientClosed, rec.Code)
	require.Equal(t, "client_disconnected", gjson.Get(rec.Body.String(), "error.type").String())
}

func TestAntigravityWriteGoogleError_ClientClosedStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", nil)

	_ = (&AntigravityGatewayService{}).writeGoogleError(c, antigravityStatusClientClosed, "Client disconnected before upstream response")

	require.Equal(t, antigravityStatusClientClosed, rec.Code)
	require.Equal(t, "CANCELLED", gjson.Get(rec.Body.String(), "error.status").String())
}

func TestAntigravityTransportError_DeadlineIsNotClientClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, canceled := range []bool{false, true} {
		for _, protocol := range []string{"claude", "gemini", "compat"} {
			name := protocol + "/deadline"
			if canceled {
				name = protocol + "/canceled"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				if canceled {
					cancel()
					ctx, cancel = context.WithCancel(context.Background())
					cancel()
				}
				defer cancel()
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				svc := newAntigravityCompatService(config.GatewayConfig{}, &httpUpstreamStub{err: ctx.Err()})
				account := newAntigravityCompatAccount(AccountTypeOAuth)
				var err error
				switch protocol {
				case "claude":
					_, err = svc.Forward(ctx, c, account, []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`), false)
				case "gemini":
					_, err = svc.ForwardGemini(ctx, c, account, "gemini-3.1-pro-high", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), false)
				case "compat":
					err = svc.handleAntigravityCompatTransportError(c, ctx.Err())
				}
				require.Error(t, err)
				wantStatus := http.StatusBadGateway
				if canceled {
					wantStatus = StatusClientClosedRequest
				}
				require.Equal(t, wantStatus, rec.Code, rec.Body.String())
				if protocol == "gemini" {
					want := "UNAVAILABLE"
					if canceled {
						want = "CANCELLED"
					}
					require.Equal(t, want, gjson.Get(rec.Body.String(), "error.status").String())
				} else {
					want := "upstream_error"
					if canceled {
						want = "client_disconnected"
					}
					require.Equal(t, want, gjson.Get(rec.Body.String(), "error.type").String())
				}
			})
		}
	}
}
