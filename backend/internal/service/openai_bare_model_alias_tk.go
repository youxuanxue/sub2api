package service

// TokenKey: non-displayed ingress rewrite for a bare OpenAI model id that has
// no standalone servable identity of its own.
//
// Unlike openai_deprecated_model_tk.go (a retirement — the old id is gone and
// the client must migrate), this is a same-lineage rename that must stay
// invisible: the client keeps sending "gpt-5.3" and TokenKey silently routes
// it to the real servable id. It cannot be expressed as an account-level
// model_mapping entry (the compiled floor is identity-only for both the
// canonical and ainzy-relay OpenAI scopes — see account_model_mapping_ssot_tk.go)
// nor as a codexModelMap entry (that map only feeds Codex-OAuth request
// transforms and billing normalization, not routing — see
// openai_codex_transform.go), so it must run at ingress, before account
// selection, on every OpenAI-compat entry point.
//
// Added 2026-07 (SSOT audit directive #3): "gpt-5.3" -> "gpt-5.3-chat-latest".

// tkOpenAIBareModelAliases maps a bare OpenAI model id to the servable id it
// silently routes to. Exact-match only — deliberately not layered through
// CanonicalizeOpenAICompatRoutingModel, since this is one known rename, not a
// family derived from suffix/spelling variants.
var tkOpenAIBareModelAliases = map[string]string{
	"gpt-5.3": "gpt-5.3-chat-latest",
}

// TkResolveOpenAIBareModelAlias returns the routable replacement for a bare
// OpenAI model id and true if the model was rewritten.
func TkResolveOpenAIBareModelAlias(model string) (string, bool) {
	resolved, ok := tkOpenAIBareModelAliases[model]
	if !ok {
		return "", false
	}
	return resolved, true
}
