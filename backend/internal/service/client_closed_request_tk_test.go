package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newClientClosedTestContext(t *testing.T, requestCtx context.Context) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if requestCtx != nil {
		req = req.WithContext(requestCtx)
	}
	c.Request = req
	return c
}

func TestIsClientClosedRequest(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	deadlineCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()

	t.Run("nil error nil context is not client closed", func(t *testing.T) {
		require.False(t, IsClientClosedRequest(nil, nil))
	})

	t.Run("deadline error is platform owned", func(t *testing.T) {
		c := newClientClosedTestContext(t, nil)
		require.False(t, IsClientClosedRequest(c, context.DeadlineExceeded))
	})

	t.Run("deadline beats wrapped cancellation and postgres text", func(t *testing.T) {
		c := newClientClosedTestContext(t, nil)
		err := fmt.Errorf("load: %w: %s", context.Canceled, "pq: canceling statement due to user request")
		require.False(t, IsClientClosedRequest(c, fmt.Errorf("query: %w: %v", context.DeadlineExceeded, err)))
	})

	t.Run("wrapped cancellation is client closed", func(t *testing.T) {
		c := newClientClosedTestContext(t, nil)
		require.True(t, IsClientClosedRequest(c, fmt.Errorf("load candidates: %w", context.Canceled)))
	})

	t.Run("canceled request context is client closed", func(t *testing.T) {
		c := newClientClosedTestContext(t, canceledCtx)
		require.True(t, IsClientClosedRequest(c, errors.New("query interrupted")))
	})

	t.Run("deadline request context stays platform owned", func(t *testing.T) {
		c := newClientClosedTestContext(t, deadlineCtx)
		require.False(t, IsClientClosedRequest(c, errors.New("query interrupted")))
	})

	t.Run("postgres cancellation text is client closed", func(t *testing.T) {
		c := newClientClosedTestContext(t, nil)
		require.True(t, IsClientClosedRequest(c, errors.New("pq: canceling statement due to user request")))
	})

	t.Run("postgres statement timeout is not client closed", func(t *testing.T) {
		c := newClientClosedTestContext(t, nil)
		require.False(t, IsClientClosedRequest(c, errors.New("pq: canceling statement due to statement timeout")))
	})

	t.Run("deadline request context beats postgres cancellation text", func(t *testing.T) {
		c := newClientClosedTestContext(t, deadlineCtx)
		require.False(t, IsClientClosedRequest(c, errors.New("pq: canceling statement due to user request")),
			"server-side deadlines must remain platform errors")
	})

	t.Run("database failure is not client closed", func(t *testing.T) {
		c := newClientClosedTestContext(t, nil)
		require.False(t, IsClientClosedRequest(c, errors.New("database unavailable")))
	})
}

func TestStatusClientClosedRequestValue(t *testing.T) {
	require.Equal(t, 499, StatusClientClosedRequest)
}
