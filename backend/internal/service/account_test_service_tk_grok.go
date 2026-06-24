package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *AccountTestService) testGrokAccountConnection(c *gin.Context, account *Account, modelID string, prompt string) error {
	if account == nil {
		return s.sendErrorAndEnd(c, "Account not found")
	}

	authToken := ""
	baseURL := ""
	switch account.Type {
	case AccountTypeAPIKey:
		authToken = account.GetOpenAIApiKey()
		baseURL = account.GetOpenAIBaseURL()
	case AccountTypeOAuth:
		authToken = account.GetGrokAccessToken()
		baseURL = account.GetGrokBaseURL()
	default:
		return s.sendErrorAndEnd(c, "Unsupported grok account type: "+account.Type)
	}
	if strings.TrimSpace(authToken) == "" {
		return s.sendErrorAndEnd(c, "No grok credential available")
	}
	if strings.TrimSpace(baseURL) == "" {
		return s.sendErrorAndEnd(c, "No grok base URL available")
	}
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid base URL: "+err.Error())
	}

	testModelID := normalizeGrokAdminTestModel(modelID)
	testModelID = account.GetMappedModel(testModelID)

	return s.testOpenAIChatCompletionsConnection(c, account, testModelID, prompt, strings.TrimRight(normalizedBaseURL, "/"), authToken)
}

func normalizeGrokAdminTestModel(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return defaultGrokTestModelID
	}
	lower := strings.ToLower(modelID)
	if strings.HasPrefix(lower, "grok") {
		if strings.HasPrefix(lower, "grok-imagine-") {
			return defaultGrokTestModelID
		}
		return modelID
	}
	return defaultGrokTestModelID
}
