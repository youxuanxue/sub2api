package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
)

// TK: Explicit target-URL helpers retained for relay/OAuth split paths and tests.
// Upstream forwardGrokResponses uses buildGrokResponsesRequest; companion callers
// (messages bridge, WS HTTP bridge, count_tokens) still resolve URL separately.

func (s *OpenAIGatewayService) resolveGrokResponsesUpstream(account *Account) (string, error) {
	if s == nil {
		return "", fmt.Errorf("openai gateway service is nil")
	}
	if account == nil {
		return "", fmt.Errorf("grok responses: account is nil")
	}
	switch {
	case account.IsGrokOAuth():
		return xai.BuildResponsesURL(account.GetGrokBaseURL())
	case account.IsGrokAPIKey():
		baseURL := account.GetOpenAIBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			return "", fmt.Errorf("grok relay account %d missing base_url", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURLForAccount(account, baseURL)
		if err != nil {
			return "", err
		}
		return buildOpenAIResponsesURL(validatedURL), nil
	default:
		return "", fmt.Errorf("grok account type %s is not supported for responses forwarding", account.Type)
	}
}

func (s *OpenAIGatewayService) grokResponsesAuthToken(ctx context.Context, c *gin.Context, account *Account) (string, error) {
	if account == nil {
		return "", fmt.Errorf("grok responses: account is nil")
	}
	switch {
	case account.IsGrokOAuth():
		token, _, err := s.getRequestCredential(ctx, c, account)
		return token, err
	case account.IsGrokAPIKey():
		token := account.GetOpenAIApiKey()
		if strings.TrimSpace(token) == "" {
			return "", fmt.Errorf("grok relay account %d missing api_key", account.ID)
		}
		return token, nil
	default:
		return "", fmt.Errorf("grok account type %s is not supported for responses forwarding", account.Type)
	}
}

func buildGrokResponsesRequestForAccount(ctx context.Context, c *gin.Context, account *Account, targetURL string, body []byte, token, cacheIdentity string) (*http.Request, error) {
	if strings.TrimSpace(targetURL) == "" {
		return nil, fmt.Errorf("grok responses target URL is empty")
	}
	req, err := http.NewRequestWithContext(grokUpstreamRequestContext(ctx, account), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if account.IsGrokOAuth() {
		applyGrokCLIHeaders(req.Header)
	}
	applyGrokCacheHeaders(req.Header, cacheIdentity)
	if c != nil {
		if v := c.GetHeader("OpenAI-Beta"); strings.TrimSpace(v) != "" {
			req.Header.Set("OpenAI-Beta", v)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	return req, nil
}
