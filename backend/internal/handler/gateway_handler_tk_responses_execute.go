package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkResponsesSelectionSessionHash prefixes sticky session hashes for Gemini-native
// OpenAI-compat groups so Responses sticky selection stays platform-isolated.
func tkResponsesSelectionSessionHash(sessionHash, groupPlatform, reqModel string) (selectionSessionHash string, groupUsesGeminiCompat bool) {
	groupUsesGeminiCompat = service.UsesGeminiNativeOpenAICompat(groupPlatform, reqModel)
	selectionSessionHash = sessionHash
	if groupUsesGeminiCompat && selectionSessionHash != "" {
		selectionSessionHash = "gemini:" + selectionSessionHash
	}
	return selectionSessionHash, groupUsesGeminiCompat
}

// tkResponsesAccountPlatformMismatch reports whether a selected account's platform
// is incompatible with a Gemini/Antigravity group platform constraint.
func tkResponsesAccountPlatformMismatch(groupPlatform string, account *service.Account) bool {
	if account == nil {
		return false
	}
	if groupPlatform == service.PlatformGemini && account.Platform != service.PlatformGemini {
		return true
	}
	if groupPlatform == service.PlatformAntigravity && account.Platform != service.PlatformAntigravity {
		return true
	}
	return false
}

// tkAttachResponsesProtocolRouting builds the canonical Responses request and
// attaches WithProtocolRouting onto requestCtx before account selection.
func (h *GatewayHandler) tkAttachResponsesProtocolRouting(
	requestCtx context.Context,
	reqModel string,
	reqStream bool,
	body []byte,
) (context.Context, error) {
	canonicalRequest, err := newCanonicalProtocolRequest(
		protocolrouter.ProtocolResponses,
		protocolrouter.ResponsesPathRoot,
		reqModel,
		reqStream,
		body,
	)
	if err != nil {
		return requestCtx, err
	}
	return service.WithProtocolRouting(requestCtx, h.protocolRouter, canonicalRequest), nil
}

func (h *GatewayHandler) executeResponsesSelectedProtocol(
	c *gin.Context,
	requestCtx context.Context,
	selection *service.AccountSelectionResult,
	account *service.Account,
	channelMapping service.ChannelMappingResult,
	reqModel string,
	parsedReq *service.ParsedRequest,
) (*service.ForwardResult, error) {
	value, executeErr := service.ExecuteSelectedProtocol(
		requestCtx,
		h.protocolRouter,
		selection,
		account,
		h.gatewayService.ValidateProtocolEndpoint,
		h.gatewayService.LoadProtocolExecutionAccount,
		service.ProtocolExecutors{
			NonGoverned: func(executionCtx context.Context, account *service.Account, _ protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				if service.UsesGeminiNativeOpenAICompat(account.Platform, reqModel) {
					if h.geminiCompatService == nil {
						return nil, errors.New("gemini compatibility service is not configured")
					}
					return h.geminiCompatService.ForwardAsResponses(executionCtx, c, account, forwardBody)
				}
				if shouldUseAntigravityCompat(account) {
					if h.antigravityGatewayService == nil {
						return nil, errors.New("antigravity compatibility service is not configured")
					}
					setActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
					return h.antigravityGatewayService.ForwardAsResponses(executionCtx, c, account, forwardBody, parsedReq)
				}
				return h.gatewayService.ForwardAsResponses(executionCtx, c, account, forwardBody, parsedReq)
			},
			ResponsesIdentity: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				openAIResult, forwardErr := h.openAIGatewayService.ForwardAsResponsesDispatched(executionCtx, c, account, forwardBody)
				return service.ForwardResultFromOpenAI(openAIResult), forwardErr
			},
			ResponsesToChat: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				openAIResult, forwardErr := h.openAIGatewayService.Forward(executionCtx, c, account, forwardBody)
				return service.ForwardResultFromOpenAI(openAIResult), forwardErr
			},
			ResponsesToMessages: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return h.gatewayService.ForwardAsResponses(executionCtx, c, account, forwardBody, parsedReq)
			},
			ResponsesToGemini: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return service.ExecuteGeminiProtocolProfile(
					plan.GeminiProfile(),
					func() (*service.ForwardResult, error) {
						return h.antigravityGatewayService.ForwardAsResponses(executionCtx, c, account, forwardBody, parsedReq)
					},
					func() (*service.ForwardResult, error) {
						return h.geminiCompatService.ForwardAsResponses(executionCtx, c, account, forwardBody)
					},
				)
			},
		},
	)
	var result *service.ForwardResult
	if value != nil {
		result, _ = value.(*service.ForwardResult)
	}
	return result, executeErr
}
