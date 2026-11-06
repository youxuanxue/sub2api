//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// runWSTerminalCase accepts one client WS, runs act on the server side, and
// returns every text frame the client saw plus the final close error.
func runWSTerminalCase(t *testing.T, act func(c *gin.Context, conn *coderws.Conn)) ([]string, coderws.CloseError) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		conn, err := coderws.Accept(c.Writer, c.Request, nil)
		require.NoError(t, err)
		service.BeginOpsStreamTurn(c, 1)
		act(c, conn)
	})
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	var frames []string
	for {
		_, payload, readErr := conn.Read(ctx)
		if readErr != nil {
			var closeErr coderws.CloseError
			require.True(t, errors.As(readErr, &closeErr), "expected close frame, got %v", readErr)
			return frames, closeErr
		}
		frames = append(frames, string(payload))
	}
}

func TestOpenAIWSFailoverExhaustedSendsTerminalErrorBeforeClose(t *testing.T) {
	resetsAt := time.Now().Add(2 * time.Hour).Unix()
	cases := []struct {
		name        string
		failover    *service.UpstreamFailoverError
		wantStatus  int
		wantType    string
		wantCode    string
		wantResets  bool
		wantRetry   string
		wantClose   coderws.StatusCode
		wantMessage string
	}{
		{
			name: "usage limit carries reset",
			failover: &service.UpstreamFailoverError{
				StatusCode:   http.StatusTooManyRequests,
				ResponseBody: []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":` + strconv.FormatInt(resetsAt, 10) + `}}`),
			},
			wantStatus: 429, wantType: "usage_limit_reached", wantCode: "usage_limit_reached", wantResets: true,
			wantClose: coderws.StatusTryAgainLater, wantMessage: service.TkClientFacingBusy429Message,
		},
		{
			name: "plain rate limit keeps retry advice, not usage limit",
			failover: &service.UpstreamFailoverError{
				StatusCode:      http.StatusTooManyRequests,
				ResponseBody:    []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit exceeded for account acct_internal_42"}}`),
				ResponseHeaders: http.Header{"Retry-After": []string{"17"}},
			},
			wantStatus: 429, wantType: "rate_limit_error", wantCode: "rate_limit_exceeded", wantRetry: "17",
			wantClose: coderws.StatusTryAgainLater, wantMessage: service.TkClientFacingBusy429Message,
		},
		{
			name:       "overload maps to retryable server_error",
			failover:   &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, ResponseBody: []byte(`{"error":{"code":"server_is_overloaded"}}`)},
			wantStatus: 503, wantType: "upstream_error", wantCode: "server_error", wantRetry: strconv.Itoa(service.OpenAICapacityShedClientRetryAfterSeconds),
			wantClose: coderws.StatusTryAgainLater, wantMessage: "upstream service temporarily unavailable",
		},
		{
			name:       "generic failure",
			failover:   &service.UpstreamFailoverError{StatusCode: http.StatusBadRequest},
			wantStatus: 502, wantType: "upstream_error", wantCode: "upstream_error",
			wantClose: coderws.StatusInternalError, wantMessage: "upstream websocket proxy failed",
		},
		{
			name:       "upstream 401 is reported as 502 so Codex keeps its own login",
			failover:   &service.UpstreamFailoverError{StatusCode: http.StatusUnauthorized},
			wantStatus: 502, wantType: "upstream_error", wantCode: "upstream_authentication_failed",
			wantClose: coderws.StatusPolicyViolation, wantMessage: "upstream websocket authentication failed",
		},
		{
			name: "credential failure",
			failover: &service.UpstreamFailoverError{
				Stage: service.GatewayFailureStageAccountAuth, Scope: service.GatewayFailureScopeAccount, Reason: service.GrokCredentialReasonRevoked,
			},
			wantStatus: 503, wantType: "api_error", wantCode: "upstream_unavailable",
			wantClose: coderws.StatusTryAgainLater, wantMessage: service.GrokCredentialUnavailableClientMessage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames, closeErr := runWSTerminalCase(t, func(c *gin.Context, conn *coderws.Conn) {
				closeOpenAIWSFailoverExhausted(c, conn, tc.failover)
			})
			require.Len(t, frames, 1, "exactly one terminal frame before close")
			frame := frames[0]
			require.Equal(t, "error", gjson.Get(frame, "type").String())
			require.Equal(t, int64(tc.wantStatus), gjson.Get(frame, "status").Int())
			require.Equal(t, tc.wantType, gjson.Get(frame, "error.type").String())
			require.Equal(t, tc.wantCode, gjson.Get(frame, "error.code").String())
			require.Equal(t, tc.wantMessage, gjson.Get(frame, "error.message").String())
			require.NotContains(t, frame, "acct_internal_42", "upstream detail must not leak")
			if tc.wantResets {
				require.Equal(t, resetsAt, gjson.Get(frame, "error.resets_at").Int())
				require.Greater(t, gjson.Get(frame, "error.resets_in_seconds").Int(), int64(0))
			} else {
				require.False(t, gjson.Get(frame, "error.resets_at").Exists())
			}
			require.Equal(t, tc.wantRetry, gjson.Get(frame, "headers.retry-after").String())
			require.Equal(t, tc.wantClose, closeErr.Code)
		})
	}
}

func TestOpenAIWSNoAvailableAccountSendsCapacityFrame(t *testing.T) {
	frames, closeErr := runWSTerminalCase(t, func(c *gin.Context, conn *coderws.Conn) {
		tkCloseOpenAIWSNoAvailableAccount(c, conn, service.ErrNoAvailableAccounts)
	})
	require.Len(t, frames, 1)
	require.Equal(t, int64(429), gjson.Get(frames[0], "status").Int())
	require.Equal(t, "api_error", gjson.Get(frames[0], "error.type").String())
	require.Equal(t, "no_available_accounts", gjson.Get(frames[0], "error.code").String())
	require.Equal(t, service.NoAvailableAccountsRetryAfterSeconds, gjson.Get(frames[0], "headers.retry-after").String())
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "no available account", closeErr.Reason)

	frames, _ = runWSTerminalCase(t, func(c *gin.Context, conn *coderws.Conn) {
		tkCloseOpenAIWSNoAvailableAccount(c, conn, errors.New("database unavailable"))
	})
	require.Len(t, frames, 1)
	require.Equal(t, int64(503), gjson.Get(frames[0], "status").Int())
	require.Equal(t, "service_unavailable", gjson.Get(frames[0], "error.code").String())
}

func TestOpenAIWSTerminalErrorWrittenOncePerTurn(t *testing.T) {
	frames, _ := runWSTerminalCase(t, func(c *gin.Context, conn *coderws.Conn) {
		frame := tkOpenAIWSSelectionTerminalError(nil)
		tkWriteOpenAIWSTerminalError(c, conn, frame)
		tkWriteOpenAIWSTerminalError(c, conn, frame)
		closeOpenAIWSFailoverExhausted(c, conn, &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests})
	})
	require.Len(t, frames, 1, "no duplicate terminal frame within one turn")
	require.Equal(t, "no_available_accounts", gjson.Get(frames[0], "error.code").String())
}
