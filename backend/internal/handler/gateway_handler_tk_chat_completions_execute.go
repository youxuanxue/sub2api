package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkChatCompletionsAccountPlatformMismatch reports whether a selected account's
// platform is incompatible with a Gemini/Antigravity group platform constraint.
func tkChatCompletionsAccountPlatformMismatch(groupPlatform string, account *service.Account) bool {
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

func (h *GatewayHandler) tkAttachChatCompletionsProtocolRouting(c *gin.Context, model string, stream bool, body []byte) error {
	canonicalRequest, err := newCanonicalProtocolRequest(
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ResponsesPathNone,
		model,
		stream,
		body,
	)
	if err != nil {
		return err
	}
	c.Request = c.Request.WithContext(service.WithProtocolRouting(c.Request.Context(), h.protocolRouter, canonicalRequest))
	return nil
}

func (h *GatewayHandler) executeChatCompletionsSelectedProtocol(
	c *gin.Context,
	requestCtx context.Context,
	selection *service.AccountSelectionResult,
	account *service.Account,
	channelMapping service.ChannelMappingResult,
	reqModel string,
	parsedReq *service.ParsedRequest,
) (*service.ForwardResult, error) {
	channelMapping = service.CandidateForwardMapping(requestCtx, channelMapping)
	value, executeErr := service.ExecuteSelectedProtocol(
		requestCtx,
		h.protocolRouter,
		selection,
		account,
		h.gatewayService.ValidateProtocolEndpoint,
		h.gatewayService.LoadProtocolExecutionAccount,
		service.ProtocolExecutors{
			ObserveOutcome: h.gatewayService.TKRecordProtocolOutcome,
			NonGoverned: func(executionCtx context.Context, account *service.Account, _ protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				return h.tkForwardChatCompletionsByOpenAIShape(
					executionCtx, c, account, reqModel, forwardBody, parsedReq,
					func() (*service.ForwardResult, error) {
						return h.gatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
					},
				)
			},
			ChatIdentity: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return h.tkForwardChatCompletionsByOpenAIShape(
					executionCtx, c, account, reqModel, forwardBody, parsedReq,
					func() (*service.ForwardResult, error) {
						openAIResult, forwardErr := h.openAIGatewayService.ForwardAsChatCompletionsDispatched(executionCtx, c, account, forwardBody, "", channelMapping.MappedModel)
						return service.ForwardResultFromOpenAI(openAIResult), forwardErr
					},
				)
			},
			ChatToResponses: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return h.tkForwardChatCompletionsByOpenAIShape(
					executionCtx, c, account, reqModel, forwardBody, parsedReq,
					func() (*service.ForwardResult, error) {
						openAIResult, forwardErr := h.openAIGatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, "", channelMapping.MappedModel)
						return service.ForwardResultFromOpenAI(openAIResult), forwardErr
					},
				)
			},
			ChatToMessages: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return h.gatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
			},
			ChatToGemini: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				forwardBody := request.Body()
				if channelMapping.Mapped {
					forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return service.ExecuteGeminiProtocolProfile(
					plan.GeminiProfile(),
					func() (*service.ForwardResult, error) {
						return h.antigravityGatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
					},
					func() (*service.ForwardResult, error) {
						return h.geminiCompatService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody)
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
