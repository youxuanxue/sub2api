package service

// wrapAntigravityCompatGeminiRequest sends Chat Completions / Responses Gemini
// traffic through the same ForwardGemini native v1internal envelope as
// generateContent. Dropping sessionId made this a different Google request and
// triggered 403 VALIDATION_REQUIRED. The client still receives Chat/Responses;
// only the upstream wire is native. Implementation owns the shared prep in
// prepareForwardGeminiWireBody.
func (s *AntigravityGatewayService) wrapAntigravityCompatGeminiRequest(projectID, model string, body []byte) ([]byte, error) {
	return s.prepareForwardGeminiWireBody(projectID, model, body)
}
