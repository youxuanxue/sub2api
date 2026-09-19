package service

// wrapAntigravityCompatGeminiRequest sends Chat Completions through the same
// native v1internal envelope as generateContent. Dropping sessionId made this
// a different Google request and triggered 403 VALIDATION_REQUIRED. The client
// still receives Chat Completions; only the upstream wire is native.
func (s *AntigravityGatewayService) wrapAntigravityCompatGeminiRequest(projectID, model string, body []byte) ([]byte, error) {
	return s.wrapV1InternalRequest(projectID, model, tkEnsureGeminiContentRoles(body))
}
