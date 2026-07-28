package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestOpenRouterProviderModels_RequiresConfiguredDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &GatewayHandler{
		gatewayService: &service.GatewayService{},
		settingService: &service.SettingService{},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/openrouter/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 5, UserID: 9})

	h.OpenRouterProviderModels(c)
	if w.Code == http.StatusOK {
		t.Fatalf("expected failure without setting repo, got %s", w.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if payload["error"] == nil {
		t.Fatalf("expected error envelope, got %s", w.Body.String())
	}
}
