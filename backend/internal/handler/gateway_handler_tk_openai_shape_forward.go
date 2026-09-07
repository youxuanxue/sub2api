package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// A bound Plan already selected the converter. Platform/model compatibility
// dispatch is only a fallback for requests outside protocol routing.
func (h *GatewayHandler) tkForwardChatCompletionsByOpenAIShape(
	executionCtx context.Context,
	c *gin.Context,
	account *service.Account,
	reqModel string,
	forwardBody []byte,
	parsedReq *service.ParsedRequest,
	openAIDefault func() (*service.ForwardResult, error),
) (*service.ForwardResult, error) {
	if _, planned := service.ProtocolExecutionPlan(executionCtx); planned {
		return openAIDefault()
	}
	switch service.ResolveGovernedOpenAIShapeMode(account, reqModel) {
	case service.GovernedOpenAIShapeGeminiCompat:
		if h.geminiCompatService == nil {
			return nil, errors.New("gemini compatibility service is not configured")
		}
		return h.geminiCompatService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody)
	case service.GovernedOpenAIShapeAntigravityClaudeRelay:
		return h.gatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
	case service.GovernedOpenAIShapeAntigravityOAuthCloudCode:
		if h.antigravityGatewayService == nil {
			return nil, errors.New("antigravity compatibility service is not configured")
		}
		setActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
		return h.antigravityGatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
	default:
		return openAIDefault()
	}
}

// Responses shares the same Plan-first boundary as Chat Completions.
func (h *GatewayHandler) tkForwardResponsesByOpenAIShape(
	executionCtx context.Context,
	c *gin.Context,
	account *service.Account,
	reqModel string,
	forwardBody []byte,
	parsedReq *service.ParsedRequest,
	openAIDefault func() (*service.ForwardResult, error),
) (*service.ForwardResult, error) {
	if _, planned := service.ProtocolExecutionPlan(executionCtx); planned {
		return openAIDefault()
	}
	switch service.ResolveGovernedOpenAIShapeMode(account, reqModel) {
	case service.GovernedOpenAIShapeGeminiCompat:
		if h.geminiCompatService == nil {
			return nil, errors.New("gemini compatibility service is not configured")
		}
		return h.geminiCompatService.ForwardAsResponses(executionCtx, c, account, forwardBody)
	case service.GovernedOpenAIShapeAntigravityClaudeRelay:
		return h.gatewayService.ForwardAsResponses(executionCtx, c, account, forwardBody, parsedReq)
	case service.GovernedOpenAIShapeAntigravityOAuthCloudCode:
		if h.antigravityGatewayService == nil {
			return nil, errors.New("antigravity compatibility service is not configured")
		}
		setActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
		return h.antigravityGatewayService.ForwardAsResponses(executionCtx, c, account, forwardBody, parsedReq)
	default:
		return openAIDefault()
	}
}
