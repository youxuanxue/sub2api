package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// A retry can select a native account after ingress entered the OpenAI handler.
// Keep credential preparation and forwarding with that account's existing owner.
func (h *OpenAIGatewayHandler) candidateProtocolExecutors(c *gin.Context, executors service.ProtocolExecutors) service.ProtocolExecutors {
	executors.ObserveOutcome = h.nativeGatewayService.TKRecordProtocolOutcome
	wrap := func(original service.ProtocolExecutionFunc) service.ProtocolExecutionFunc {
		return func(ctx context.Context, account *service.Account, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
			if service.CandidateRequestFromContext(ctx) == nil || service.IsOpenAICompatPlatform(account.Platform) {
				return original(ctx, account, plan, request)
			}
			return h.forwardCandidateNative(ctx, c, account, request)
		}
	}
	executors.NonGoverned = wrap(executors.NonGoverned)
	if executors.MessagesIdentity != nil {
		executors.MessagesIdentity = wrap(executors.MessagesIdentity)
	}
	if executors.ChatToMessages != nil {
		executors.ChatToMessages = wrap(executors.ChatToMessages)
	}
	if executors.ResponsesToMessages != nil {
		executors.ResponsesToMessages = wrap(executors.ResponsesToMessages)
	}
	return executors
}

func (h *OpenAIGatewayHandler) forwardCandidateNative(ctx context.Context, c *gin.Context, account *service.Account, request protocolrouter.CanonicalRequest) (*service.OpenAIForwardResult, error) {
	if h.nativeGatewayService == nil {
		return nil, service.ErrProtocolExecutorMissing
	}
	body := request.Body()
	var result *service.ForwardResult
	var err error
	switch request.InboundProtocol() {
	case protocolrouter.ProtocolMessages:
		if account.Platform == service.PlatformAntigravity && account.Type != service.AccountTypeAPIKey {
			result, err = h.antigravityGatewayService.Forward(ctx, c, account, body, false)
		} else if account.Platform == service.PlatformGemini {
			result, err = h.geminiCompatService.Forward(ctx, c, account, body)
		} else {
			var parsed *service.ParsedRequest
			parsed, err = service.ParseGatewayRequest(service.NewRequestBodyRef(body), service.PlatformAnthropic)
			if err == nil {
				c.Set("parsed_request", parsed)
				result, err = h.nativeGatewayService.Forward(ctx, c, account, parsed)
			}
		}
	case protocolrouter.ProtocolChatCompletions:
		if account.Platform == service.PlatformGemini {
			result, err = h.geminiCompatService.ForwardAsChatCompletions(ctx, c, account, body)
		} else if account.Platform == service.PlatformAntigravity && account.Type != service.AccountTypeAPIKey {
			result, err = h.antigravityGatewayService.ForwardAsChatCompletions(ctx, c, account, body, nil)
		} else {
			result, err = h.nativeGatewayService.ForwardAsChatCompletions(ctx, c, account, body, nil)
		}
	case protocolrouter.ProtocolResponses:
		if account.Platform == service.PlatformGemini {
			result, err = h.geminiCompatService.ForwardAsResponses(ctx, c, account, body)
		} else if account.Platform == service.PlatformAntigravity && account.Type != service.AccountTypeAPIKey {
			result, err = h.antigravityGatewayService.ForwardAsResponses(ctx, c, account, body, nil)
		} else {
			result, err = h.nativeGatewayService.ForwardAsResponses(ctx, c, account, body, nil)
		}
	default:
		return nil, service.ErrProtocolExecutorMissing
	}
	return service.OpenAIForwardResultFromForward(result), err
}
