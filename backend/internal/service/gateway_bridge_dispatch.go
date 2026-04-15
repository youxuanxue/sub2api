package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	BridgeEndpointChatCompletions = "chat_completions"
	BridgeEndpointResponses       = "responses"
	BridgeEndpointEmbeddings      = "embeddings"
	BridgeEndpointImages          = "images"
)

var (
	bridgeDispatchTotal  atomic.Int64
	bridgeDispatchErrors atomic.Int64
)

func BridgeDispatchStats() (total int64, errors int64) {
	return bridgeDispatchTotal.Load(), bridgeDispatchErrors.Load()
}

func recordBridgeDispatch() {
	bridgeDispatchTotal.Add(1)
}

func recordBridgeDispatchError() {
	bridgeDispatchErrors.Add(1)
}

// accountUsesNewAPIAdaptorBridge is the single gate for Tier1 New API adaptor dispatch.
// True when the account has a channel type (channel_type > 0)—required for the fifth
// platform `newapi` and optional on legacy four-platform accounts—and the endpoint is
// supported, and the runtime kill switch allows it.
func accountUsesNewAPIAdaptorBridge(settings *SettingService, account *Account, endpoint string) bool {
	if account == nil || account.ChannelType <= 0 {
		return false
	}
	if settings != nil && !settings.IsNewAPIBridgeEnabled(context.Background()) {
		return false
	}
	switch endpoint {
	case BridgeEndpointChatCompletions, BridgeEndpointResponses, BridgeEndpointEmbeddings, BridgeEndpointImages:
		return true
	default:
		return false
	}
}

// ShouldDispatchToNewAPIBridge reports whether this request should enter the New API adaptor path.
func (s *GatewayService) ShouldDispatchToNewAPIBridge(account *Account, endpoint string) bool {
	var st *SettingService
	if s != nil {
		st = s.settingService
	}
	return accountUsesNewAPIAdaptorBridge(st, account, endpoint)
}

func errBridgeMissingCredential(field string) *newapitypes.NewAPIError {
	return newapitypes.NewErrorWithStatusCode(
		fmt.Errorf("account is missing %s for New API adaptor relay", field),
		newapitypes.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		newapitypes.ErrOptionWithSkipRetry(),
	)
}

// ForwardAsChatCompletionsDispatched is the Tier1 bridge boundary for chat/completions.
func (s *GatewayService) ForwardAsChatCompletionsDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	parsed *ParsedRequest,
) (*ForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions) {
		return s.ForwardAsChatCompletions(ctx, c, account, body, parsed)
	}
	recordBridgeDispatch()
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}
	out, apiErr := bridge.DispatchChatCompletions(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointChatCompletions),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("gateway.newapi_bridge_dispatch",
		zap.String("endpoint", BridgeEndpointChatCompletions),
		zap.Int("channel_type", account.ChannelType),
		zap.String("bridge_path", "newapi_adaptor"),
		zap.String("adaptor_relay_format", bridge.DescribeRelayFormat(out.AdaptorRelayFmt)),
		zap.Int("adaptor_api_type", out.AdaptorAPIType),
		zap.Int64("account_id", account.ID),
	)
	upstreamModel := strings.TrimSpace(out.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = out.Model
	}
	return &ForwardResult{
		Model:         out.Model,
		UpstreamModel: upstreamModel,
		Stream:        out.Stream,
		Duration:      out.Duration,
		Usage:         claudeUsageFromNewAPIDTO(out.Usage),
	}, nil
}

// ForwardAsResponsesDispatched is the Tier1 bridge boundary for /responses.
func (s *GatewayService) ForwardAsResponsesDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	parsed *ParsedRequest,
) (*ForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointResponses) {
		return s.ForwardAsResponses(ctx, c, account, body, parsed)
	}
	recordBridgeDispatch()
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}
	out, apiErr := bridge.DispatchResponses(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointResponses),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("gateway.newapi_bridge_dispatch",
		zap.String("endpoint", BridgeEndpointResponses),
		zap.Int("channel_type", account.ChannelType),
		zap.String("bridge_path", "newapi_adaptor"),
		zap.String("adaptor_relay_format", bridge.DescribeRelayFormat(out.AdaptorRelayFmt)),
		zap.Int("adaptor_api_type", out.AdaptorAPIType),
		zap.Int64("account_id", account.ID),
	)
	upstreamModel := strings.TrimSpace(out.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = out.Model
	}
	return &ForwardResult{
		Model:         out.Model,
		UpstreamModel: upstreamModel,
		Stream:        out.Stream,
		Duration:      out.Duration,
		Usage:         claudeUsageFromNewAPIDTO(out.Usage),
	}, nil
}
