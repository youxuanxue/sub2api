package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func (s *OpenAIGatewayService) resolveGrokInputTokensUpstream(account *Account) (string, error) {
	if s == nil {
		return "", fmt.Errorf("openai gateway service is nil")
	}
	if account == nil {
		return "", fmt.Errorf("grok input_tokens: account is nil")
	}
	switch {
	case account.IsGrokOAuth():
		validatedBaseURL, err := xai.ValidatedBaseURL(account.GetGrokBaseURL())
		if err != nil {
			return "", err
		}
		return buildOpenAIResponsesInputTokensURL(validatedBaseURL), nil
	case account.IsGrokAPIKey():
		baseURL := account.GetOpenAIBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			return "", fmt.Errorf("grok relay account %d missing base_url", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURLForAccount(account, baseURL)
		if err != nil {
			return "", err
		}
		return buildOpenAIResponsesInputTokensURL(validatedURL), nil
	default:
		return "", fmt.Errorf("grok account type %s is not supported for input_tokens forwarding", account.Type)
	}
}
