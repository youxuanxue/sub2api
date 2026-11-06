//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkAttributeOpsUpstreamAccount(t *testing.T) {
	selected := int64(374)
	tests := []struct {
		name   string
		events []*service.OpsUpstreamErrorEvent
		want   *int64
	}{
		{name: "no events keeps selected account", events: nil, want: &selected},
		{
			name: "last event account wins over selected",
			events: []*service.OpsUpstreamErrorEvent{
				{AccountID: 351, UpstreamStatusCode: http.StatusTooManyRequests},
				{AccountID: 393, UpstreamStatusCode: http.StatusTooManyRequests},
			},
			want: ptrInt64(393),
		},
		{
			name: "trailing nil is skipped",
			events: []*service.OpsUpstreamErrorEvent{
				{AccountID: 351, UpstreamStatusCode: http.StatusTooManyRequests},
				nil,
			},
			want: ptrInt64(351),
		},
		{
			name: "account-less last event keeps selected account",
			events: []*service.OpsUpstreamErrorEvent{
				{AccountID: 351, UpstreamStatusCode: http.StatusTooManyRequests},
				{UpstreamStatusCode: http.StatusBadGateway},
			},
			want: &selected,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := selected
			entry := &service.OpsInsertErrorLogInput{AccountID: &id, UpstreamErrors: tt.events}
			tkAttributeOpsUpstreamAccount(entry, false)
			require.Equal(t, *tt.want, *entry.AccountID)
		})
	}
	require.NotPanics(t, func() { tkAttributeOpsUpstreamAccount(nil, false) })

	t.Run("stream failure keeps the streaming account", func(t *testing.T) {
		id := selected
		entry := &service.OpsInsertErrorLogInput{AccountID: &id, UpstreamErrors: []*service.OpsUpstreamErrorEvent{
			{AccountID: 351, UpstreamStatusCode: http.StatusTooManyRequests},
		}}
		tkAttributeOpsUpstreamAccount(entry, true)
		require.Equal(t, selected, *entry.AccountID)
	})
}

// Prod 2026-11-06 shape: a quota 429 on earlier hops, then the client
// disconnects while the last selected account is still working. The 499 row
// must name the account that returned the 429, matching upstream_status_code.
func TestOpsErrorLoggerMiddleware_ClientClosedAttributesToFailingAccount(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 1)
	gin.SetMode(gin.TestMode)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set(opsAccountIDKey, int64(374))
		c.Set(service.OpsUpstreamStatusCodeKey, http.StatusTooManyRequests)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{
			{AccountID: 351, UpstreamStatusCode: http.StatusTooManyRequests, Kind: "failover", Message: "You have exceeded the 5-hour usage quota."},
			{AccountID: 393, UpstreamStatusCode: http.StatusTooManyRequests, Kind: "failover", Message: "You have exceeded the weekly usage quota."},
		})
		c.JSON(service.StatusClientClosedRequest, gin.H{"error": gin.H{"type": "api_error", "message": "client closed request"}})
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	router.ServeHTTP(httptest.NewRecorder(), req)

	job := <-opsErrorLogQueue
	require.Equal(t, service.StatusClientClosedRequest, job.entry.StatusCode)
	require.NotNil(t, job.entry.AccountID)
	require.Equal(t, int64(393), *job.entry.AccountID)
	require.NotNil(t, job.entry.UpstreamErrorMessage)
	require.Contains(t, *job.entry.UpstreamErrorMessage, "weekly usage quota")
}
