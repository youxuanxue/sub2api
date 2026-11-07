//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// geminiRealHTTPUpstream sends through a real http.Client so request-context
// cancellation reaches the httptest server exactly as in production.
type geminiRealHTTPUpstream struct {
	target string
	calls  atomic.Int32
}

func (u *geminiRealHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = strings.TrimPrefix(u.target, "http://")
	out.Host = ""
	out.RequestURI = ""
	return http.DefaultClient.Do(out)
}

func (u *geminiRealHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

// TestGeminiNonStreamClientDisconnectStillBills cancels the caller while Google
// is still generating: the upstream must finish, usage must come back for
// settlement, and the request is classified 499 per client-closed-499-ssot.
func TestGeminiNonStreamClientDisconnectStillBills(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const geminiBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":13,"totalTokenCount":24}}`

	for _, protocol := range []string{"native", "messages", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			arrived := make(chan struct{}, 1)
			release := make(chan struct{})
			var upstreamCanceled atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				arrived <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					upstreamCanceled.Store(true)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(geminiBody))
			}))
			defer server.Close()

			upstream := &geminiRealHTTPUpstream{target: server.URL}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{
				Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}},
			}}
			account := &Account{ID: 7, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-key", "base_url": server.URL}}

			clientCtx, cancelClient := context.WithCancel(context.Background())
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			var body string
			switch protocol {
			case "native":
				body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
			case "responses":
				body = `{"model":"gemini-2.5-flash","input":"hello"}`
			default:
				body = `{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`
			}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(body)).WithContext(clientCtx)

			go func() {
				<-arrived
				cancelClient() // client leaves mid-generation
				time.Sleep(50 * time.Millisecond)
				close(release)
			}()

			var result *ForwardResult
			var err error
			switch protocol {
			case "native":
				result, err = svc.ForwardNative(clientCtx, c, account, "gemini-2.5-flash", "generateContent", false, []byte(body))
			case "messages":
				result, err = svc.Forward(clientCtx, c, account, []byte(body))
			case "chat":
				result, err = svc.ForwardAsChatCompletions(clientCtx, c, account, []byte(body))
			case "responses":
				result, err = svc.ForwardAsResponses(clientCtx, c, account, []byte(body))
			}

			require.NoError(t, err)
			require.False(t, upstreamCanceled.Load(), "upstream request must not be cancelled by client disconnect")
			require.NotNil(t, result)
			require.Equal(t, 11, result.Usage.InputTokens)
			require.Equal(t, 13, result.Usage.OutputTokens)
			require.True(t, HasOpsClientClosedRequest(c), "client-gone completion is classified 499")
			require.Equal(t, int32(1), upstream.calls.Load())
		})
	}
}

func TestGeminiNonStreamCallerGoneStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, tkGeminiCallerGoneBeforeRetry(ctx, 2))
	cancel()
	require.NoError(t, tkGeminiCallerGoneBeforeRetry(ctx, 1), "first attempt is owned by handler pre-checks")
	require.ErrorIs(t, tkGeminiCallerGoneBeforeRetry(ctx, 2), context.Canceled)

	detached, release := tkGeminiNonStreamUpstreamContext(ctx, false)
	defer release()
	require.NoError(t, detached.Err(), "non-stream upstream ctx is detached")
	same, _ := tkGeminiNonStreamUpstreamContext(ctx, true)
	require.Equal(t, ctx, same, "stream/countTokens keep caller ctx")
}

func TestGeminiMarkClientGoneSkipsDeadlineAndStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	tkGeminiMarkClientGoneAfterNonStream(c, deadlineCtx, false)
	require.False(t, HasOpsClientClosedRequest(c), "deadline is platform-owned, not 499")

	canceled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	tkGeminiMarkClientGoneAfterNonStream(c, canceled, true)
	require.False(t, HasOpsClientClosedRequest(c), "stream paths own their own disconnect handling")
	tkGeminiMarkClientGoneAfterNonStream(c, canceled, false)
	require.True(t, HasOpsClientClosedRequest(c))
}
