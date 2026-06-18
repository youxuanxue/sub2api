package service

// thinking_ref_model_tk.go — TokenKey weld for the "which model do the thinking
// helpers key on?" decision.
//
// The upstream-owned thinking machinery (ResolveThinkingProtocol,
// FilterThinkingBlocks, FilterThinkingBlocksForRetry, RectifyThinkingBudget,
// ShouldRectifyThinkingSignatureError …) all take a single `modelID string`
// argument that they treat as the "thinking-block reference model" — the model
// whose family decides the thinking-block contract (Anthropic-strict signature
// rules vs passback-required round-trip vs unknown/no-strip). See
// thinking_protocol.go.
//
// TokenKey relays the same Anthropic /v1/messages request shape down TWO
// structurally different paths, and the correct reference model is the OPPOSITE
// variable on each:
//
//   - Native Anthropic relay (gateway_service.go / gateway_request.go /
//     gateway_service_tk_signature_preempt.go): the upstream provider IS
//     Anthropic, so the MAPPED upstream model (mappedModel / reqModel) is the
//     authority on the thinking contract. Pass the mapped model.
//
//   - Anthropic-shaped client → non-Anthropic backend compat
//     (gemini_messages_compat_service.go: a client speaks /v1/messages but is
//     routed to a Gemini backend): the body being filtered is still Anthropic
//     shape, but the MAPPED model is a Gemini model that
//     ResolveThinkingProtocol would classify as non-strict — so it would skip
//     the very filtering the Anthropic-dialect body requires. The ORIGINAL
//     client model is the authority here. Pass the original model.
//
// These two helpers are identity functions by value, but they NAME the choice
// so it is greppable and self-documenting: a future upstream merge that
// silently swaps mappedModel↔originalModel at a call site has to rename the
// helper to compile-match the path, making the inversion visible in review
// instead of a silent behavior flip (the exact failure class the
// relay-invariant registry guards). The behavior on both paths is pinned by
// TestThinkingRefModel_AnthropicRelayUsesMappedModel and
// TestThinkingRefModel_AnthropicCompatUsesOriginalModel, registered in
// scripts/sentinels/relay-invariants.json.
//
// NOTE: do NOT "simplify" these to a single helper or inline them back to the
// bare variable — the whole point is that the two paths must pick DIFFERENT
// models and the seam must stay legible across upstream merges.

// thinkingRefModelForAnthropicRelay returns the model ID the thinking helpers
// must key on for the NATIVE Anthropic relay path: the mapped upstream model.
func thinkingRefModelForAnthropicRelay(mappedModel string) string {
	return mappedModel
}

// thinkingRefModelForAnthropicCompat returns the model ID the thinking helpers
// must key on for the Anthropic-shaped-client → non-Anthropic-backend compat
// path (e.g. gemini_messages_compat): the original client model, because the
// thinking-block SHAPE is set by the client's Anthropic dialect, not the mapped
// backend model.
func thinkingRefModelForAnthropicCompat(originalModel string) string {
	return originalModel
}
