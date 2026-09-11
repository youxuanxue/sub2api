package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayHandlerImages_DisabledGroupRejectsBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(111)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      222,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: false,
		},
		User: &service.User{ID: 333},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 333, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}

	h.Images(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}

func TestOpenAIImageHandlersRejectUnpricedRequestedSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, handle := range map[string]func(*OpenAIGatewayHandler, *gin.Context){"images": (*OpenAIGatewayHandler).Images, "generations": (*OpenAIGatewayHandler).ImageGenerations} {
		for _, tc := range []struct{ pricedTier, size string }{{"1K", "2048x2048"}, {"2K", "1024x1024"}} {
			t.Run(name+"/"+tc.size, func(t *testing.T) {
				cfg := &config.Config{}
				billing := service.NewBillingService(cfg, nil)
				gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, billing, nil, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
				price := 0.25
				group := &service.Group{ID: 111, Platform: service.PlatformOpenAI, AllowImageGeneration: true, ModelPricing: []service.ChannelModelPricing{{Models: []string{"gpt-image-2"}, BillingMode: service.BillingModeImage, Intervals: []service.PricingInterval{{TierLabel: tc.pricedTier, PerRequestPrice: &price}}}}}
				// The account/repository dependencies remain absent: rejection must happen
				// before scheduling or forwarding despite another image size being priced.
				h := &OpenAIGatewayHandler{cfg: cfg, gatewayService: gateway, billingCacheService: &service.BillingCacheService{}, apiKeyService: &service.APIKeyService{}, concurrencyHelper: &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}}}
				req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw","size":%q}`, tc.size)))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = req
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 222, GroupID: &group.ID, Group: group, User: &service.User{ID: 333}})
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 333, Concurrency: 1})
				handle(h, c)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "has no image generation price")
			})
		}
	}
}
