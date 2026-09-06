package service

// GovernedOpenAIShapeMode selects which forwarder governed ChatIdentity /
// ResponsesIdentity should use. Protocol routing marks Antigravity edge-relay
// stubs as governed, but their Gemini chat traffic must not enter
// OpenAIGatewayService (GetOpenAIProtocolAPIKey rejects platform=antigravity).
type GovernedOpenAIShapeMode int

const (
	// GovernedOpenAIShapeOpenAI keeps the OpenAI gateway path.
	GovernedOpenAIShapeOpenAI GovernedOpenAIShapeMode = iota
	// GovernedOpenAIShapeGeminiCompat hops via GeminiMessagesCompatService
	// (Antigravity apikey → {base}/antigravity/v1beta/...; Gemini native).
	GovernedOpenAIShapeGeminiCompat
	// GovernedOpenAIShapeAntigravityClaudeRelay hops Claude chat/responses
	// through GatewayService → {GetBaseURL()}/v1/messages on the edge stub.
	GovernedOpenAIShapeAntigravityClaudeRelay
)

// ResolveGovernedOpenAIShapeMode mirrors NonGoverned chat/responses branching
// for governed OpenAI-shape identity adapters.
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
	return GovernedOpenAIShapeOpenAI
}
