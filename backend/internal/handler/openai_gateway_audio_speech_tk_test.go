//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAudioSpeech_UnpricedModelRejectedWithoutForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"model":"totally-unknown-tts-model","input":"你好"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(9101)
	userID := int64(9102)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      9103,
		GroupID: &groupID,
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
		},
		User: &service.User{ID: userID, Status: service.StatusActive},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService: service.NewOpenAIGatewayServiceForUnitTests(&service.BillingService{}, &config.Config{RunMode: config.RunModeSimple}),
		billingCacheService: service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil),
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper: &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(
			&helperConcurrencyCacheStub{userSeq: []bool{true}},
		)},
		cfg: &config.Config{RunMode: config.RunModeSimple},
	}

	h.AudioSpeech(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "totally-unknown-tts-model")
	require.Contains(t, strings.ToLower(rec.Body.String()), "unpriced")
}
