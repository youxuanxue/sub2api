package service

import "net/http"

const antigravityOpenAICompatMessagesRelayUnavailableBody = `{"error":{"type":"server_error","message":"Antigravity OpenAI-compatible Claude routing requires an API-key edge relay account"},"type":"error"}`

func antigravityOpenAICompatMessagesRelayFailover(account *Account) *UpstreamFailoverError {
	if account == nil || account.Platform != PlatformAntigravity {
		return nil
	}
	if account.Type == AccountTypeAPIKey && account.GetBaseURL() != "" {
		return nil
	}
	return &UpstreamFailoverError{
		StatusCode:   http.StatusServiceUnavailable,
		ResponseBody: []byte(antigravityOpenAICompatMessagesRelayUnavailableBody),
	}
}
