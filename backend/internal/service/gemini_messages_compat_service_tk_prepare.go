package service

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
)

// tkPrepareGeminiMessagesForward applies TokenKey group Claude→Gemini dispatch
// mapping and the priced-serving gate before convert/forward.
func (s *GeminiMessagesCompatService) tkPrepareGeminiMessagesForward(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	originalModel string,
	reqModel string,
) (mappedReqModel string, err error) {
	mappedReqModel = reqModel
	// TK: 分组级 Claude→Gemini 模型映射 (gemini_messages_dispatch_tk.go)。
	// handler 通过 gin.Context 透传 *Group；resolver 仅当 group 非空且
	// 配置了映射规则、且 req.Model 不是 gemini-* 形态时才改写 req.Model。
	if g := tkGroupFromGinContext(c); g != nil {
		if mapped := g.TKResolveGeminiDispatchModel(mappedReqModel); mapped != "" {
			mappedReqModel = mapped
		}
	}
	// TK priced-serving gate (docs/approved/priced-or-it-doesnt-ship.md): reject
	// unpriced models with a 404 BEFORE forward / stream start (SSE pre-flight).
	// No-op unless account.Platform is in the enabled set (gemini ships first).
	// Forward serves an Anthropic /v1/messages ingress (writeClaudeError elsewhere),
	// so the 404 body must be the ANTHROPIC envelope (BLOCKER4: byte-align to the
	// client's wire protocol, not account.Platform). Judge originalModel — billing
	// records on result.Model=originalModel here (forwardResultBillingModel), so the
	// gate must use the exact key billing charges (BLOCKER1). See
	// gateway_priced_serving_gate_tk.go.
	if !s.tkPricedServingGate(ctx, c, tkGateWireAnthropic, account.Platform, originalModel, originalModel) {
		return mappedReqModel, fmt.Errorf("priced serving gate: model %q not priced for platform %q", originalModel, account.Platform)
	}
	return mappedReqModel, nil
}

// tkPrepareGeminiNativeForward runs the priced-serving gate for native Gemini
// wire (Gemini error envelope) before ForwardNative continues. countTokens is
// exempt (docs §4, BLOCKER5): zero-billing pre-flight must not 404.
func (s *GeminiMessagesCompatService) tkPrepareGeminiNativeForward(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	originalModel string,
	action string,
) error {
	if action == "countTokens" {
		return nil
	}
	// TK priced-serving gate (docs/approved/priced-or-it-doesnt-ship.md): reject unpriced
	// models before native forward. No-op unless account.Platform is in the enabled set.
	// See gateway_priced_serving_gate_tk.go.
	if !s.tkPricedServingGate(ctx, c, tkGateWireGemini, account.Platform, originalModel, originalModel) {
		return fmt.Errorf("priced serving gate: model %q not priced for platform %q", originalModel, account.Platform)
	}
	return nil
}
