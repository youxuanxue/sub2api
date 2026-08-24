package service

// TK: newapi bridge ownership for inbound /v1/chat/completions.
//
// Same defect class as the /v1/messages leak fixed in #1800, one endpoint over.
//
// Every OpenAI-shaped forwarder below ForwardAsChatCompletions resolves its
// upstream through OpenAI-family accessors (nativeOpenAIBaseURLForAccount ->
// Account.GetOpenAIBaseURL), which return "" for platform=newapi: a newapi
// channel is neither IsOpenAI nor IsCNProvider. Before #1800 the caller turned
// that "" into https://api.openai.com and POSTed the channel's real credential
// (DeepSeek, DashScope, Ark, ...) to OpenAI, which answers "Incorrect API key
// provided" — a routing defect that reads like a dead credential. #1800 added
// the fail-closed guard in openAIChatCompletionsTargetURL, so the same request
// now returns ErrForeignCredentialOfficialOpenAIFallback instead of leaking.
//
// That guard is the last line of defense, not the fix. #1800 closed the
// /v1/messages path by giving bridge-eligible newapi accounts to
// ForwardAsAnthropicDispatched before any OpenAI-shaped fallback could claim
// them. /v1/chat/completions never got the same treatment:
// ForwardAsChatCompletionsDispatched existed with zero callers — unwired dead
// code, exactly as ForwardAsAnthropicDispatched had been.
//
// Prod consequence (2026-08-24, prod on 1.8.172): account 39 "ds-官"
// (platform=newapi, channel_type=43, base_url=https://api.deepseek.com,
// status=active, schedulable=true, zero Redis load) failed 100% of
// /v1/chat/completions requests in ~35ms — far too fast for any network round
// trip, because no request was ever sent. Gateway logs carried one line per
// failure:
//
//	handler/openai_chat_completions.go  openai_chat_completions.forward_failed
//	account_id=39 model=deepseek-v4-pro
//	error="refusing to send foreign account credential to api.openai.com:
//	       account has no resolved base_url"
//
// The group that account served had no other member, so every failure emptied
// the pool and the next request fast-failed with routing 429 "No available
// accounts for platform newapi" — 614 of those in one 30-minute window, all on
// one API key, all one model. Two hours earlier (pre-1.8.172) the same account
// produced "Upstream authentication failed" instead: the leak had been live and
// quiet for as long as this path existed, and #1800 only made it loud.
//
// The admin account test passed against the same account throughout, because
// dispatchNewAPIAccountTestChatCompletions goes through the bridge and reads the
// channel's own base URL. Same account, same endpoint name, two resolvers.
//
// This file restores the #1800 invariant for the Chat Completions entry: a
// bridge-eligible newapi account is relayed by the adaptor, which reads that
// account's channel base URL and key, and never reaches a resolver that can
// return "".

import (
	"context"

	"github.com/gin-gonic/gin"
)

// newAPIBridgeOwnsChatCompletions reports whether inbound /v1/chat/completions
// for this account must relay through the NewAPI adaptor bridge.
//
// Mirrors newAPIBridgeOwnsAnthropicMessages (the #1800 predicate) so both entry
// points share one rule: platform must be newapi, and the account must be
// bridge-eligible for the Chat Completions capability.
//
// Accounts that are NOT bridge-eligible are deliberately left alone. The
// VolcEngine Agent Plan carve-out in ShouldDispatchToNewAPIBridge returns false
// so its /api/plan/v3 endpoint keeps TokenKey's native path (the upstream
// adaptor would append its own /api/v3 suffix); such accounts already resolve a
// base URL through nativeOpenAIBaseURLForAccount's agent-plan branch and are
// unaffected by this predicate.
func (s *OpenAIGatewayService) newAPIBridgeOwnsChatCompletions(account *Account) bool {
	if account == nil || account.Platform != PlatformNewAPI {
		return false
	}
	return s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions)
}

// tkTryRouteChatCompletionsViaNewAPIBridge claims bridge-eligible newapi
// accounts for the adaptor before any OpenAI-shaped forwarder can.
//
// Returns handled=false for every other account so the caller proceeds with its
// existing routing chain unchanged.
//
// No recursion: ForwardAsChatCompletionsDispatched falls back to
// ForwardAsChatCompletions only when ShouldDispatchToNewAPIBridge is false,
// while this function is entered only when it is true. Same argument as #1800's
// newAPIBridgeOwnsAnthropicMessages.
func (s *OpenAIGatewayService) tkTryRouteChatCompletionsViaNewAPIBridge(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, bool, error) {
	if !s.newAPIBridgeOwnsChatCompletions(account) {
		return nil, false, nil
	}
	result, err := s.ForwardAsChatCompletionsDispatched(ctx, c, account, body, promptCacheKey, defaultMappedModel)
	return result, true, err
}
