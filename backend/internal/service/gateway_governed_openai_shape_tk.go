package service

// GovernedOpenAIShapeMode selects which forwarder OpenAI-shape chat/responses
// adapters should use. Protocol routing marks Antigravity edge-relay stubs as
// governed, but their Gemini chat traffic must not enter OpenAIGatewayService
// (GetOpenAIProtocolAPIKey rejects platform=antigravity).
//
// This is the single owner for Gemini-compat / Antigravity apikey relay /
// Antigravity OAuth Cloud Code branching. NonGoverned and governed identity
// adapters both call ResolveGovernedOpenAIShapeMode; only the residual
// OpenAI/default arm differs by path (gatewayService vs openAIGatewayService).
type GovernedOpenAIShapeMode int

const (
	// GovernedOpenAIShapeOpenAI keeps the residual OpenAI/default path.
	GovernedOpenAIShapeOpenAI GovernedOpenAIShapeMode = iota
	// GovernedOpenAIShapeGeminiCompat hops via GeminiMessagesCompatService
	// (Antigravity apikey → {base}/antigravity/v1beta/...; Gemini native).
	GovernedOpenAIShapeGeminiCompat
	// GovernedOpenAIShapeAntigravityClaudeRelay hops Claude chat/responses
	// through GatewayService → {GetBaseURL()}/v1/messages on the edge stub.
	GovernedOpenAIShapeAntigravityClaudeRelay
	// GovernedOpenAIShapeAntigravityOAuthCloudCode hops via
	// AntigravityGatewayService (Cloud Code generateContent).
	GovernedOpenAIShapeAntigravityOAuthCloudCode
)

// ResolveGovernedOpenAIShapeMode is the SSOT for OpenAI-shape chat/responses
// special-case branching shared by NonGoverned and governed identity adapters.
func ResolveGovernedOpenAIShapeMode(account *Account, model string) GovernedOpenAIShapeMode {
	if account == nil {
		return GovernedOpenAIShapeOpenAI
	}
	if UsesGeminiNativeOpenAICompat(account.Platform, model) {
		return GovernedOpenAIShapeGeminiCompat
	}
	if account.Platform == PlatformAntigravity && account.Type == AccountTypeAPIKey {
		return GovernedOpenAIShapeAntigravityClaudeRelay
	}
	if account.Platform == PlatformAntigravity && account.Type == AccountTypeOAuth {
		return GovernedOpenAIShapeAntigravityOAuthCloudCode
	}
	return GovernedOpenAIShapeOpenAI
}
