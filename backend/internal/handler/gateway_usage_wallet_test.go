//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageWalletFailUserRepo struct {
	service.UserRepository
}

func (usageWalletFailUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return nil, errors.New("temporary user lookup failure")
}

func TestGatewayHandlerUsage_WalletFallsBackToAuthenticatedBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := usageWalletFailUserRepo{}
	billingCache := service.NewBillingCacheService(nil, repo, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billingCache.Stop)

	user := &service.User{ID: 31, Balance: 12.75, Status: service.StatusActive}
	apiKey := &service.APIKey{
		ID:     47,
		UserID: user.ID,
		User:   user,
		Status: service.StatusAPIKeyActive,
	}
	h := &GatewayHandler{
		userService:         service.NewUserService(repo, nil, nil, nil),
		billingCacheService: billingCache,
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})

	h.Usage(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "unrestricted", response["mode"])
	require.Equal(t, 12.75, response["balance"])
	require.Equal(t, 12.75, response["remaining"])
}
