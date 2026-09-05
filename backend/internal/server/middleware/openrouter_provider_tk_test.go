//go:build unit

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type orProviderSettingRepoStub struct {
	service.SettingRepository
	value string
}

func (s *orProviderSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyTKOpenRouterProviderConfig {
		return s.value, nil
	}
	return "", nil
}

func TestMaybeRewriteOpenRouterProviderChatBody_UniversalKeyBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfgJSON, _ := json.Marshal(service.OpenRouterProviderConfig{
		Enabled:       true,
		ModelIDPrefix: "tokenkey/",
		BillingUserID: 32,
	})
	settingSvc := service.NewSettingService(&orProviderSettingRepoStub{value: string(cfgJSON)}, nil)

	apiKey := &service.APIKey{
		ID:          1,
		Name:        service.OpenRouterProviderSellerKeyName,
		UserID:      32,
		User:        &service.User{ID: 32},
		RoutingMode: service.RoutingModeUniversal,
	}

	body := []byte(`{"model":"tokenkey/claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(bytes.NewReader(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	MaybeRewriteOpenRouterProviderChatBody(c, apiKey, settingSvc)

	gotBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(gotBody), `"model":"claude-sonnet-4-6"`) {
		t.Fatalf("expected rewritten internal model, got %s", string(gotBody))
	}
}

func TestMaybeRewriteOpenRouterProviderChatBody_NonBillingUserNoRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfgJSON, _ := json.Marshal(service.OpenRouterProviderConfig{
		Enabled:       true,
		ModelIDPrefix: "tokenkey/",
		BillingUserID: 32,
	})
	settingSvc := service.NewSettingService(&orProviderSettingRepoStub{value: string(cfgJSON)}, nil)

	apiKey := &service.APIKey{
		ID:     2,
		Name:   "other",
		UserID: 99,
		User:   &service.User{ID: 99},
	}
	body := []byte(`{"model":"tokenkey/claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(bytes.NewReader(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	MaybeRewriteOpenRouterProviderChatBody(c, apiKey, settingSvc)

	gotBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(gotBody), `"model":"tokenkey/claude-sonnet-4-6"`) {
		t.Fatalf("non-billing key must not rewrite, got %s", string(gotBody))
	}
}
