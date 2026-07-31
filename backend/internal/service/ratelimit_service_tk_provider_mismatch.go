package service

import (
	"net/http"
	"strings"
)

// tkIsNewAPIAgentPlanOpenAIProviderMismatch identifies an impossible provider
// response: an Agent Plan account was selected, but OpenAI's official API-key
// help text came back. This is a gateway routing defect, not evidence that the
// Ark credential is invalid, so account-health penalties must not persist it.
func tkIsNewAPIAgentPlanOpenAIProviderMismatch(account *Account, statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusUnauthorized || !isNewAPIVolcEngineAgentPlanAccount(account) {
		return false
	}
	body := strings.ToLower(string(responseBody))
	return strings.Contains(body, "incorrect api key provided") &&
		strings.Contains(body, "platform.openai.com/account/api-keys")
}
