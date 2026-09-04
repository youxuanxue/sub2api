package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func prepareGatewayMessagesExecution(
	c *gin.Context,
	gatewayService *service.GatewayService,
	account *service.Account,
	groupID *int64,
	parsed *service.ParsedRequest,
	mapping service.ChannelMappingResult,
	request protocolrouter.CanonicalRequest,
) (*service.ParsedRequest, []byte, error) {
	executionParsed := *parsed
	if err := executionParsed.ReplaceBody(request.Body()); err != nil {
		return nil, nil, err
	}
	if mapping.Mapped {
		executionParsed.Model = mapping.MappedModel
		if err := executionParsed.ReplaceBody(gatewayService.ReplaceModelInBody(executionParsed.Body.Bytes(), mapping.MappedModel)); err != nil {
			return nil, nil, err
		}
	}
	if err := executionParsed.ReplaceBody(gatewayService.ApplyBedrockCCCompat(c, executionParsed.Body.Bytes(), executionParsed.Model, account, groupID)); err != nil {
		return nil, nil, err
	}
	c.Set("parsed_request", &executionParsed)
	return &executionParsed, executionParsed.Body.Bytes(), nil
}

func (h *GatewayHandler) tkAttachMessagesProtocolRouting(c *gin.Context, model string, stream bool, body []byte) error {
	canonicalRequest, err := newCanonicalProtocolRequest(
		protocolrouter.ProtocolMessages,
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

func (h *GatewayHandler) executeMessagesSelectedProtocol(
	c *gin.Context,
	requestCtx context.Context,
	selection *service.AccountSelectionResult,
	account *service.Account,
	apiKey *service.APIKey,
	attemptParsedReq *service.ParsedRequest,
	channelMapping service.ChannelMappingResult,
	hasBoundSession bool,
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
				executionParsedReq, attemptBody, prepareErr := prepareGatewayMessagesExecution(c, h.gatewayService, account, apiKey.GroupID, attemptParsedReq, channelMapping, request)
				if prepareErr != nil {
					return nil, prepareErr
				}
				if account.Platform == service.PlatformAntigravity && account.Type != service.AccountTypeAPIKey {
					return h.antigravityGatewayService.Forward(executionCtx, c, account, attemptBody, hasBoundSession)
				}
				return h.gatewayService.Forward(executionCtx, c, account, executionParsedReq)
			},
			MessagesIdentity: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				executionParsedReq, _, prepareErr := prepareGatewayMessagesExecution(c, h.gatewayService, account, apiKey.GroupID, attemptParsedReq, channelMapping, request)
				if prepareErr != nil {
					return nil, prepareErr
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return h.gatewayService.Forward(executionCtx, c, account, executionParsedReq)
			},
			MessagesToResponses: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				_, attemptBody, prepareErr := prepareGatewayMessagesExecution(c, h.gatewayService, account, apiKey.GroupID, attemptParsedReq, channelMapping, request)
				if prepareErr != nil {
					return nil, prepareErr
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				openAIResult, forwardErr := h.openAIGatewayService.ForwardAsAnthropic(executionCtx, c, account, attemptBody, "", channelMapping.MappedModel)
				return service.ForwardResultFromOpenAI(openAIResult), forwardErr
			},
			MessagesToChat: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				_, attemptBody, prepareErr := prepareGatewayMessagesExecution(c, h.gatewayService, account, apiKey.GroupID, attemptParsedReq, channelMapping, request)
				if prepareErr != nil {
					return nil, prepareErr
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				openAIResult, forwardErr := h.openAIGatewayService.ForwardAsAnthropicDispatched(executionCtx, c, account, attemptBody, "", channelMapping.MappedModel)
				return service.ForwardResultFromOpenAI(openAIResult), forwardErr
			},
			MessagesToGemini: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				_, attemptBody, prepareErr := prepareGatewayMessagesExecution(c, h.gatewayService, account, apiKey.GroupID, attemptParsedReq, channelMapping, request)
				if prepareErr != nil {
					return nil, prepareErr
				}
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return service.ExecuteGeminiProtocolProfile(
					plan.GeminiProfile(),
					func() (*service.ForwardResult, error) {
						return h.antigravityGatewayService.Forward(executionCtx, c, account, attemptBody, hasBoundSession)
					},
					func() (*service.ForwardResult, error) {
						return h.geminiCompatService.Forward(executionCtx, c, account, attemptBody)
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
