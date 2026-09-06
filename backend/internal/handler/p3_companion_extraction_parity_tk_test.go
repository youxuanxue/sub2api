//go:build unit

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestP3Companion_RespondCompactNotSupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{}

	t.Run("non-legacy ignores", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
		handled := h.tkRespondCompactNotSupported(c, service.ErrNoAvailableCompactAccounts, false, false)
		require.False(t, handled)
		require.Equal(t, 0, w.Body.Len())
	})

	t.Run("wrong error ignores", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
		handled := h.tkRespondCompactNotSupported(c, errors.New("other"), true, false)
		require.False(t, handled)
		require.Equal(t, 0, w.Body.Len())
	})

	t.Run("legacy compact empty pool writes compact_not_supported", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
		handled := h.tkRespondCompactNotSupported(c, service.ErrNoAvailableCompactAccounts, true, false)
		require.True(t, handled)
		require.Contains(t, w.Body.String(), "compact_not_supported")
	})
}
