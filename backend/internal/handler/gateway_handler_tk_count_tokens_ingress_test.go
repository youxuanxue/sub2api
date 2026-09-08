//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayCountTokensIngressPlatformGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, groupPlatform, resolvedPlatform, forcedPlatform, guardPlatform string
		model, routingMode                                                   string
		wantStatus                                                           int
		wantMessage                                                          string
	}{
		{name: "antigravity_direct", groupPlatform: service.PlatformAntigravity, wantStatus: http.StatusOK},
		{name: "universal_antigravity_backing_group", groupPlatform: service.PlatformAntigravity, routingMode: service.RoutingModeUniversal, wantStatus: http.StatusOK},
		{name: "anthropic_rejects_gemini", groupPlatform: service.PlatformAnthropic, wantStatus: http.StatusBadRequest, wantMessage: service.TkUnsupportedModelMessage("gemini-3.8-flash")},
		{name: "antigravity_unknown_model", groupPlatform: service.PlatformAntigravity, model: "gemini-does-not-exist", wantStatus: http.StatusBadRequest, wantMessage: service.TkUnsupportedModelMessage("gemini-does-not-exist")},
		{name: "antigravity_body_guard", groupPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAntigravity, wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "pre-flight limit"},
		{name: "anthropic_body_guard", groupPlatform: service.PlatformAnthropic, model: "claude-sonnet-4-6", guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "pre-flight limit"},
		{name: "antigravity_ignores_anthropic_guard", groupPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusOK},
		{name: "resolved_antigravity_body_guard", groupPlatform: service.PlatformComposite, resolvedPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAntigravity, wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "pre-flight limit"},
		{name: "resolved_anthropic_rejects_gemini", groupPlatform: service.PlatformComposite, resolvedPlatform: service.PlatformAnthropic, wantStatus: http.StatusBadRequest, wantMessage: service.TkUnsupportedModelMessage("gemini-3.8-flash")},
		{name: "forced_antigravity_overrides_anthropic", groupPlatform: service.PlatformAnthropic, resolvedPlatform: service.PlatformAnthropic, forcedPlatform: service.PlatformAntigravity, guardPlatform: service.PlatformAnthropic, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := tc.model
			if model == "" {
				model = "gemini-3.8-flash"
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Gateway.UpstreamBodyGuards = []config.UpstreamBodyGuardConfig{{Platform: tc.guardPlatform, RejectBytes: 1}}
			h, group, usage, upstream := newAntigravityIngressHarness(t, tc.groupPlatform, protocolrouter.ProtocolGeminiGenerateContent, cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"abcdefghijklmnop"}]}`, model)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
			ctx := c.Request.Context()
			if tc.resolvedPlatform != "" {
				ctx = service.WithResolvedTargetPlatform(ctx, tc.resolvedPlatform)
			}
			if tc.forcedPlatform != "" {
				ctx = context.WithValue(ctx, ctxkey.ForcePlatform, tc.forcedPlatform)
			}
			c.Request = c.Request.WithContext(ctx)
			key := &service.APIKey{ID: 219, GroupID: &group.ID, Group: group, RoutingMode: tc.routingMode, User: &service.User{ID: 1}}
			c.Set(string(middleware2.ContextKeyAPIKey), key)
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
			h.CountTokens(c)
			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			require.Nil(t, upstream.request)
			require.Empty(t, usage.logs)
			if tc.wantStatus != http.StatusOK {
				require.Contains(t, rec.Body.String(), tc.wantMessage)
				require.Zero(t, c.GetInt64(opsAccountIDKey))
				return
			}
			require.JSONEq(t, `{"input_tokens":4}`, rec.Body.String())
			require.Equal(t, int64(62), c.GetInt64(opsAccountIDKey))
		})
	}
}
