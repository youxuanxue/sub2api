package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ShouldDispatchToNewAPIBridge reports whether this OpenAI-gateway request should use the New API adaptor path.
func (s *OpenAIGatewayService) ShouldDispatchToNewAPIBridge(account *Account, endpoint string) bool {
	var st *SettingService
	if s != nil {
		st = s.settingService
	}
	return accountUsesNewAPIAdaptorBridge(st, account, endpoint)
}

// ForwardAsChatCompletionsDispatched is the Tier1 bridge boundary for /chat/completions in OpenAI gateway.
func (s *OpenAIGatewayService) ForwardAsChatCompletionsDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions) {
		return s.ForwardAsChatCompletions(ctx, c, account, body, promptCacheKey, defaultMappedModel)
	}
	recordBridgeDispatch()
	// Sticky routing for newapi bridge: derive a key (or accept client-sent one),
	// inject prompt_cache_key into body AND set X-Session-Id header on the gin
	// request so GLM-style adaptors can pick it up. See docs/approved/sticky-routing.md.
	body = applyStickyToNewAPIBridge(ctx, c, s.settingService, account, body, "")
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}
	out, apiErr := bridge.DispatchChatCompletions(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointChatCompletions),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("openai_gateway.newapi_bridge_dispatch",
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
	return &OpenAIForwardResult{
		Model:         out.Model,
		UpstreamModel: upstreamModel,
		Stream:        out.Stream,
		Duration:      out.Duration,
		Usage:         openAIUsageFromNewAPIDTO(out.Usage),
	}, nil
}

// ForwardAsResponsesDispatched is the Tier1 bridge boundary for /responses in OpenAI gateway.
func (s *OpenAIGatewayService) ForwardAsResponsesDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointResponses) {
		return s.Forward(ctx, c, account, body)
	}
	recordBridgeDispatch()
	body = applyStickyToNewAPIBridge(ctx, c, s.settingService, account, body, "")
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}
	out, apiErr := bridge.DispatchResponses(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointResponses),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("openai_gateway.newapi_bridge_dispatch",
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
	return &OpenAIForwardResult{
		Model:         out.Model,
		UpstreamModel: upstreamModel,
		Stream:        out.Stream,
		Duration:      out.Duration,
		Usage:         openAIUsageFromNewAPIDTO(out.Usage),
	}, nil
}

// ForwardAsEmbeddingsDispatched is the Tier1 bridge boundary for /v1/embeddings in OpenAI gateway.
func (s *OpenAIGatewayService) ForwardAsEmbeddingsDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointEmbeddings) {
		return s.ForwardAsEmbeddings(ctx, c, account, body, defaultMappedModel)
	}
	recordBridgeDispatch()
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}
	out, apiErr := bridge.DispatchEmbeddings(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointEmbeddings),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("openai_gateway.newapi_bridge_dispatch",
		zap.String("endpoint", BridgeEndpointEmbeddings),
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
	return &OpenAIForwardResult{
		Model:         out.Model,
		UpstreamModel: upstreamModel,
		Stream:        false,
		Duration:      out.Duration,
		Usage:         openAIUsageFromNewAPIDTO(out.Usage),
	}, nil
}

// ForwardAsImageGenerationsDispatched is the Tier1 bridge boundary for /v1/images/generations in OpenAI gateway.
func (s *OpenAIGatewayService) ForwardAsImageGenerationsDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointImages) {
		return s.ForwardAsImageGenerations(ctx, c, account, body, defaultMappedModel)
	}
	recordBridgeDispatch()
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}
	out, apiErr := bridge.DispatchImageGenerations(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointImages),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("openai_gateway.newapi_bridge_dispatch",
		zap.String("endpoint", BridgeEndpointImages),
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
	return &OpenAIForwardResult{
		Model:         out.Model,
		UpstreamModel: upstreamModel,
		Stream:        false,
		Duration:      out.Duration,
		Usage:         openAIUsageFromNewAPIDTO(out.Usage),
	}, nil
}
