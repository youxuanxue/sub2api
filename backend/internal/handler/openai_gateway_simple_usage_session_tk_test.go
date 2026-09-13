package handler

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAISimpleForwardUsagePersistsClientSession(t *testing.T) {
	for _, session := range []string{"audio-session-123", ""} {
		t.Run(session, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			repo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
			svc := service.NewOpenAIGatewayService(nil, repo, nil, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			h := &OpenAIGatewayHandler{gatewayService: svc}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/audio/speech", nil)
			if session != "" {
				c.Request.Header.Set("X-Session-Id", session)
			}
			key := &service.APIKey{ID: 22, User: &service.User{ID: 1}}
			h.tkSubmitOpenAISimpleForwardUsage(tkOpenAISimpleUsageSubmitInput{
				C: c, APIKey: key, Account: &service.Account{ID: 137, Platform: service.PlatformOpenAI},
				Result:   &service.OpenAIForwardResult{RequestID: "grok_audio:provider-id", Model: "gpt-4o", Usage: service.OpenAIUsage{InputTokens: 2, OutputTokens: 1}},
				ReqModel: "gpt-4o",
			})
			select {
			case row := <-repo.created:
				require.Equal(t, int64(22), row.APIKeyID)
				require.Equal(t, "grok_audio:provider-id", row.RequestID)
				if session == "" {
					require.Nil(t, row.SessionID)
				} else {
					require.NotNil(t, row.SessionID)
					require.Equal(t, session, *row.SessionID)
				}
			case <-time.After(time.Second):
				t.Fatal("usage row was not recorded")
			}
		})
	}
}
