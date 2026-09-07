package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkForwardChatCompletionsByOpenAIShape dispatches chat/completions through the
// ResolveGovernedOpenAIShapeMode SSOT. openAIDefault covers the residual arm
// (NonGoverned → gatewayService; governed identity → openAIGatewayService).
func (h *GatewayHandler) tkForwardChatCompletionsByOpenAIShape(
	executionCtx context.Context,
	c *gin.Context,
	account *service.Account,
	reqModel string,
	forwardBody []byte,
	parsedReq *service.ParsedRequest,
	openAIDefault func() (*service.ForwardResult, error),
) (*service.ForwardResult, error) {
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

// tkForwardResponsesByOpenAIShape dispatches /v1/responses through the same
// ResolveGovernedOpenAIShapeMode SSOT as chat/completions.
func (h *GatewayHandler) tkForwardResponsesByOpenAIShape(
	executionCtx context.Context,
	c *gin.Context,
	account *service.Account,
	reqModel string,
	forwardBody []byte,
	parsedReq *service.ParsedRequest,
	openAIDefault func() (*service.ForwardResult, error),
) (*service.ForwardResult, error) {
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
