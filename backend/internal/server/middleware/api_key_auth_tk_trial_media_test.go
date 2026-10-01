//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMaybeBlockTrialUnpaidMedia_BlocksImagesForTrial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	key := &service.APIKey{
		ID:     1,
		UserID: 9,
		User:   &service.User{ID: 9, Role: service.RoleUser, Balance: 1, TotalRecharged: 0},
	}

	// nil SettingService → defaults (blocked=true, maxBalance=2)
	require.True(t, MaybeBlockTrialUnpaidMedia(c, key, nil))
	require.Equal(t, http.StatusPaymentRequired, w.Code)
	require.Contains(t, w.Body.String(), "trial_unpaid_media_blocked")
}

func TestMaybeBlockTrialUnpaidMedia_AllowsPaidAndText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("paid user images", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		key := &service.APIKey{User: &service.User{Role: service.RoleUser, Balance: 0.5, TotalRecharged: 10}}
		require.False(t, MaybeBlockTrialUnpaidMedia(c, key, nil))
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("trial chat", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		key := &service.APIKey{User: &service.User{Role: service.RoleUser, Balance: 1, TotalRecharged: 0}}
		require.False(t, MaybeBlockTrialUnpaidMedia(c, key, nil))
	})

	t.Run("trial video poll", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/task-1", nil)
		key := &service.APIKey{User: &service.User{Role: service.RoleUser, Balance: 1, TotalRecharged: 0}}
		require.False(t, MaybeBlockTrialUnpaidMedia(c, key, nil))
	})
}
