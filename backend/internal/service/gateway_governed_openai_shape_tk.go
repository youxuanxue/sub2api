package service

// GovernedOpenAIShapeMode preserves platform compatibility dispatch only when
// no protocol Plan is bound. Governed execution uses the Plan's adapter instead.
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

// ResolveGovernedOpenAIShapeMode selects the legacy chat/responses forwarder
// for requests outside protocol routing.
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
