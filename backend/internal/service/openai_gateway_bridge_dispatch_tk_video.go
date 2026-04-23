package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BridgeEndpointVideoSubmit / BridgeEndpointVideoFetch identify the video
// submit + fetch task endpoints on the OpenAI-compat / newapi platforms.
// They participate in the standard accountUsesNewAPIAdaptorBridge gate the
// same way chat/responses/embeddings/images do (channel_type > 0 + setting
// kill switch).
const (
	BridgeEndpointVideoSubmit = "video_submit"
	BridgeEndpointVideoFetch  = "video_fetch"
)

// VideoSubmitDispatchedResult mirrors OpenAIForwardResult for video task
// submission. The handler uses Outcome to persist the registry record and
// build the public response. UpstreamModel may differ from Model when the
// channel has model_mapping configured.
type VideoSubmitDispatchedResult struct {
	Outcome  *bridge.TaskSubmitOutcome
	Model    string
	Stream   bool
	Account  *Account
	GroupID  int64
	UserID   int64
	APIKeyID int64
}

// ForwardAsVideoSubmitDispatched is the bridge boundary for
// POST /v1/video/generations (and the OpenAI-compat alias /v1/videos).
//
// Unlike chat/embeddings which have a non-bridge sibling, video generation is
// only available through the New API task adaptor (no native sub2api code
// path). Therefore we hard-fail when the account is not bridge-eligible —
// silently routing to a non-existent native path would surface as a
// confusing 5xx.
func (s *OpenAIGatewayService) ForwardAsVideoSubmitDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*bridge.TaskSubmitOutcome, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointVideoSubmit) {
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("channel_type")}
	}
	if !bridge.IsVideoSupportedChannelType(account.ChannelType) {
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("video task adaptor")}
	}
	recordBridgeDispatch()
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}

	out, apiErr := bridge.DispatchVideoSubmit(ctx, c, in, body)
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointVideoSubmit),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("openai_gateway.newapi_bridge_dispatch",
		zap.String("endpoint", BridgeEndpointVideoSubmit),
		zap.Int("channel_type", account.ChannelType),
		zap.String("bridge_path", "newapi_adaptor"),
		zap.String("upstream_task_id", out.UpstreamTaskID),
		zap.Int64("account_id", account.ID),
	)
	return out, nil
}

// ForwardAsVideoFetchDispatched is the bridge boundary for
// GET /v1/video/generations/:task_id (and the OpenAI-compat alias).
// Inputs come from a registry lookup (VideoTaskRegistry); the dispatch is
// account-agnostic because the registry already knows where to route.
func (s *OpenAIGatewayService) ForwardAsVideoFetchDispatched(
	ctx context.Context,
	c *gin.Context,
	in bridge.VideoFetchInput,
) (*bridge.VideoFetchOutcome, error) {
	if in.ChannelType <= 0 || strings.TrimSpace(in.UpstreamTaskID) == "" {
		return nil, errors.New("video fetch requires channel_type and upstream_task_id")
	}
	if !bridge.IsVideoSupportedChannelType(in.ChannelType) {
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("video task adaptor")}
	}
	out, apiErr := bridge.DispatchVideoFetch(ctx, c, in)
	if apiErr != nil {
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", BridgeEndpointVideoFetch),
			zap.Int("channel_type", in.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.String("upstream_task_id", in.UpstreamTaskID),
		)
		return nil, &NewAPIRelayError{Err: apiErr}
	}
	logger.L().Info("openai_gateway.newapi_bridge_dispatch",
		zap.String("endpoint", BridgeEndpointVideoFetch),
		zap.Int("channel_type", in.ChannelType),
		zap.String("bridge_path", "newapi_adaptor"),
		zap.String("upstream_task_id", in.UpstreamTaskID),
		zap.String("status", out.Status),
	)
	return out, nil
}

// accountUsesNewAPIAdaptorBridgeVideo extends the existing endpoint switch in
// gateway_bridge_dispatch.go to recognize the video endpoints. It is wired in
// via TkAccountUsesNewAPIBridgeForVideo so the canonical
// accountUsesNewAPIAdaptorBridge keeps a small, easy-to-audit switch; tests
// for video gating live next to this helper.
func TkAccountUsesNewAPIBridgeForVideo(settings *SettingService, account *Account, endpoint string) bool {
	switch endpoint {
	case BridgeEndpointVideoSubmit, BridgeEndpointVideoFetch:
	default:
		return false
	}
	if account == nil || account.ChannelType <= 0 {
		return false
	}
	if settings != nil && !settings.IsNewAPIBridgeEnabled(context.Background()) {
		return false
	}
	return true
}
