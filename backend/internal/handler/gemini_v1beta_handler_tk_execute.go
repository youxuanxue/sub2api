package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkAttachGeminiGenerateContentProtocolRouting builds the canonical Gemini
// generateContent request and attaches WithProtocolRouting before scheduling.
// CountTokens stays outside this generation router (caller skips this helper).
func (h *GatewayHandler) tkAttachGeminiGenerateContentProtocolRouting(
	c *gin.Context,
	modelName string,
	stream bool,
	body []byte,
) error {
	canonicalRequest, err := newCanonicalProtocolRequest(
		protocolrouter.ProtocolGeminiGenerateContent,
		protocolrouter.ResponsesPathNone,
		modelName,
		stream,
		body,
	)
	if err != nil {
		return err
	}
	c.Request = c.Request.WithContext(service.WithProtocolRouting(c.Request.Context(), h.protocolRouter, canonicalRequest))
	return nil
}

func (h *GatewayHandler) executeGeminiV1BetaSelectedProtocol(
	c *gin.Context,
	requestCtx context.Context,
	selection *service.AccountSelectionResult,
	account *service.Account,
	modelName, action string,
	stream bool,
	hasBoundSession bool,
	sessionGroupID int64,
	sessionKey string,
	cleanThoughtSignatures bool,
) (*service.ForwardResult, error) {
	modelName = service.CandidateEffectiveModel(requestCtx, modelName)
	forwardNonGoverned := func(executionCtx context.Context, executionAccount *service.Account, request protocolrouter.CanonicalRequest) (any, error) {
		forwardBody := request.Body()
		if cleanThoughtSignatures {
			forwardBody = service.CleanGeminiNativeThoughtSignatures(forwardBody)
		}
		if executionAccount.Platform == service.PlatformAntigravity && executionAccount.Type != service.AccountTypeAPIKey {
			return h.antigravityGatewayService.ForwardGemini(
				executionCtx,
				c,
				executionAccount,
				modelName,
				action,
				stream,
				forwardBody,
				hasBoundSession,
				service.WithForwardGeminiSession(sessionGroupID, sessionKey),
			)
		}
		return h.geminiCompatService.ForwardNative(executionCtx, c, executionAccount, modelName, action, stream, forwardBody)
	}
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
				return forwardNonGoverned(executionCtx, account, request)
			},
			GeminiToChat: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				return h.openAIGatewayService.ForwardGeminiViaChat(executionCtx, c, account, request)
			},
			GeminiIdentity: func(executionCtx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
				setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
				forwardBody := request.Body()
				// Repair only the transport copy after the immutable plan is validated.
				if cleanThoughtSignatures {
					forwardBody = service.CleanGeminiNativeThoughtSignatures(forwardBody)
				}
				return service.ExecuteGeminiProtocolProfile(
					plan.GeminiProfile(),
					func() (*service.ForwardResult, error) {
						return h.antigravityGatewayService.ForwardGemini(
							executionCtx, c, account, modelName, action, stream, forwardBody, hasBoundSession,
							service.WithForwardGeminiSession(sessionGroupID, sessionKey),
						)
					},
					func() (*service.ForwardResult, error) {
						return h.geminiCompatService.ForwardNative(executionCtx, c, account, modelName, action, stream, forwardBody)
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

// CountTokens is a native, unmetered operation, outside the generation Plan.
// Account selection and admission still run in GeminiV1BetaModels before this call.
func (h *GatewayHandler) forwardGeminiCountTokens(c *gin.Context, ctx context.Context, account *service.Account, model string, body []byte) (*service.ForwardResult, error) {
	model = service.CandidateEffectiveModel(ctx, model)
	if account.Platform == service.PlatformAntigravity && account.Type != service.AccountTypeAPIKey {
		return h.antigravityGatewayService.ForwardGemini(ctx, c, account, model, "countTokens", false, body, false)
	}
	return h.geminiCompatService.ForwardNative(ctx, c, account, model, "countTokens", false, body)
}
