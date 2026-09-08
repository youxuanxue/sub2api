package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type candidateSnapshotBillingRepository struct {
	service.UsageBillingRepository
	commands chan *service.UsageBillingCommand
}

func (r *candidateSnapshotBillingRepository) Apply(_ context.Context, command *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.commands <- command
	return &service.UsageBillingApplyResult{}, nil
}

func TestCandidateBillingSnapshotSurvivesNextTurnBeforeWorkerRuns(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	pool.Submit(func(context.Context) { close(blocked); <-release })
	<-blocked
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	repo := &candidateSnapshotBillingRepository{commands: make(chan *service.UsageBillingCommand, 1)}
	usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
	cfg := &config.Config{}
	gateway := service.NewOpenAIGatewayService(nil, usage, repo, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	handler := &OpenAIGatewayHandler{gatewayService: gateway, usageRecordWorkerPool: pool}
	group := &service.Group{ID: 11, RateMultiplier: 2, SubscriptionType: service.SubscriptionTypeSubscription}
	key := &service.APIKey{ID: 7, UserID: 9, RoutingMode: service.RoutingModeUniversal, Group: group, GroupID: &group.ID, User: &service.User{ID: 9}}
	subscription := &service.UserSubscription{ID: 12}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestedPublicModel, "gpt-4"))
	handler.tkSubmitHTTPForwardUsage(&service.OpenAIForwardResult{RequestID: "turn-1", Model: "gpt-4", Usage: service.OpenAIUsage{InputTokens: 1000}}, tkHTTPForwardUsageSubmitInput{
		C: c, APIKey: key, Subscription: subscription, Account: &service.Account{ID: 63, Platform: service.PlatformOpenAI}, ReqModel: "gpt-4",
		ChannelMapping: service.ChannelMappingResult{ChannelID: 33, MappedModel: "gpt-4", BillingModelSource: "requested"},
	})
	group.ID, group.RateMultiplier, group.SubscriptionType = 99, .01, service.SubscriptionTypeStandard
	subscription.ID = 100
	key.Group = &service.Group{ID: 99, RateMultiplier: .01}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestedPublicModel, "gpt-5"))
	close(release)
	select {
	case command := <-repo.commands:
		require.NotNil(t, command.SubscriptionID)
		require.Equal(t, int64(12), *command.SubscriptionID)
		require.Positive(t, command.SubscriptionCost)
		require.Zero(t, command.BalanceCost)
	case <-time.After(3 * time.Second):
		t.Fatal("usage worker did not record the original subscription")
	}
	select {
	case log := <-usage.created:
		require.Equal(t, "gpt-4", log.RequestedModel)
		require.NotNil(t, log.ChannelID)
		require.Equal(t, int64(33), *log.ChannelID)
	case <-time.After(3 * time.Second):
		t.Fatal("usage worker did not record the original request model")
	}
}
