package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Video bridge endpoint identifiers. Lives in the TK companion (not in the
// upstream-shape gateway_bridge_dispatch.go) so adding/removing TK-only
// endpoints is a single-file change with no upstream merge surface.
//
// The canonical accountUsesNewAPIAdaptorBridge gate delegates here via
// tkBridgeEndpointEnabled — so the upstream-shape file is unaware of which
// extra endpoints TK has added beyond the four it natively recognizes.
const (
	BridgeEndpointVideoSubmit = "video_submit"
	BridgeEndpointVideoFetch  = "video_fetch"
)

// tkBridgeEndpointEnabled reports whether the endpoint name is one of the
// TK-extended bridge endpoints. The nil-account / channel_type>0 / kill-switch
// preconditions are already enforced by accountUsesNewAPIAdaptorBridge before
// this is consulted; we only answer the endpoint-name question.
func tkBridgeEndpointEnabled(endpoint string) bool {
	switch endpoint {
	case BridgeEndpointVideoSubmit, BridgeEndpointVideoFetch:
		return true
	default:
		return false
	}
}

// errBridgeVideoUnsupportedChannel returns a precise 400 to the client when
// the selected account's channel_type has no upstream task adaptor registered
// (e.g. an OpenAI account asked to do video). This is a configuration error,
// not a missing-credential error — we do NOT reuse errBridgeMissingCredential
// because the latter implies the operator forgot to populate something.
func errBridgeVideoUnsupportedChannel(channelType int) *newapitypes.NewAPIError {
	return newapitypes.NewErrorWithStatusCode(
		errors.New("selected account's channel_type does not have a video task adaptor"),
		newapitypes.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		newapitypes.ErrOptionWithSkipRetry(),
	)
}

// ForwardAsVideoSubmitDispatched is the bridge boundary for
// POST /v1/video/generations (and the OpenAI-compat alias /v1/videos).
//
// Unlike chat/embeddings which have a non-bridge sibling, video generation is
// only available through the New API task adaptor (no native sub2api code
// path). Therefore we hard-fail when the account is not bridge-eligible —
// silently routing to a non-existent native path would surface as a
// confusing 5xx.
//
// publicTaskID MUST be the registry-stable id the caller will persist; the
// bridge stamps it onto the wire response so the synchronous POST body
// matches the registry record (and the GET /v1/videos/:task_id alias).
func (s *OpenAIGatewayService) ForwardAsVideoSubmitDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	publicTaskID string,
	body []byte,
) (*bridge.TaskSubmitOutcome, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointVideoSubmit) {
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("channel_type")}
	}
	if !bridge.IsVideoSupportedChannelType(account.ChannelType) {
		return nil, &NewAPIRelayError{Err: errBridgeVideoUnsupportedChannel(account.ChannelType)}
	}
	recordBridgeDispatch()
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}

	out, apiErr := bridge.DispatchVideoSubmit(ctx, c, in, publicTaskID, body)
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
		return nil, &NewAPIRelayError{Err: errBridgeVideoUnsupportedChannel(in.ChannelType)}
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
