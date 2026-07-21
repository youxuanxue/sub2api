package service

// TokenKey: strip Claude Code / Codex context-window model alias suffixes (e.g.
// "gpt-5.5[1m]") from OpenAI-compat inbound model ids before routing, Codex
// transform, and upstream forward.
//
// Why this lives on the OpenAI-compat path
// ----------------------------------------
// Claude Code denotes the 1M-context variant with a trailing "[1m]" alias on
// BOTH Anthropic ids (claude-opus-4-8[1m]) and OpenAI-compat dispatch ids
// (gpt-5.5[1m]). CC uses the suffix locally to pick a 1M client window; the
// upstream Codex/OpenAI model id must stay bare ("gpt-5.5").
//
// Without stripping on the /v1/messages → OpenAI Responses bridge:
//   - upstream may reject the bracketed id or silently cap to ~200K;
//   - billing/routing resolve a non-catalog alias;
//   - CC shows 200K while the user selected a "1M context" custom model.
//
// The Anthropic direct forward path already strips via gateway_anthropic_
// context_window_alias_tk.go. This companion applies the same pure stripper to
// the OpenAI-compat normalization seam (NormalizeOpenAICompatRequestedModel,
// applyOpenAICompatModelNormalization, normalizeCodexModel, messages-dispatch
// mapped-model normalization).
//
// Safety contract: identical to tkStripContextWindowModelAlias — trailing
// bracketed alias only, idempotent, bare ids are byte-for-byte no-ops.

func applyOpenAICompatContextWindowModelAlias(model string) (string, bool) {
	return tkStripContextWindowModelAlias(model)
}
