package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

type anthropicPassthroughAuthKind int

const (
	anthropicPassthroughAuthAPIKey anthropicPassthroughAuthKind = iota
	anthropicPassthroughAuthOAuth
)

func (s *GatewayService) forwardAnthropicOAuthPassthroughWithInput(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	input anthropicPassthroughForwardInput,
) (*ForwardResult, error) {
	return s.forwardAnthropicPassthroughWithInput(ctx, c, account, input, anthropicPassthroughAuthOAuth)
}

func (s *GatewayService) buildAnthropicPassthroughUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
	authKind anthropicPassthroughAuthKind,
) (*http.Request, []byte, error) {
	switch authKind {
	case anthropicPassthroughAuthOAuth:
		return s.buildUpstreamRequestAnthropicOAuthPassthrough(ctx, c, account, body, token)
	default:
		return s.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, token)
	}
}

func (s *GatewayService) buildAnthropicPassthroughCountTokensRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
	authKind anthropicPassthroughAuthKind,
) (*http.Request, error) {
	switch authKind {
	case anthropicPassthroughAuthOAuth:
		return s.buildCountTokensRequestAnthropicOAuthPassthrough(ctx, c, account, body, token)
	default:
		return s.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, token)
	}
}

func (s *GatewayService) anthropicOAuthPassthroughMessagesURL(account *Account) (string, error) {
	targetURL := claudeAPIURL
	if account.IsCustomBaseURLEnabled() {
		customURL := account.GetCustomBaseURL()
		if customURL == "" {
			return "", fmt.Errorf("custom_base_url is enabled but not configured for account %d", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURL(customURL)
		if err != nil {
			return "", err
		}
		targetURL = s.buildCustomRelayURL(validatedURL, "/v1/messages", account)
	}
	return targetURL, nil
}

func (s *GatewayService) anthropicOAuthPassthroughCountTokensURL(account *Account) (string, error) {
	targetURL := claudeAPICountTokensURL
	if account.IsCustomBaseURLEnabled() {
		customURL := account.GetCustomBaseURL()
		if customURL == "" {
			return "", fmt.Errorf("custom_base_url is enabled but not configured for account %d", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURL(customURL)
		if err != nil {
			return "", err
		}
		targetURL = s.buildCustomRelayURL(validatedURL, "/v1/messages/count_tokens", account)
	}
	return targetURL, nil
}

func setAnthropicOAuthPassthroughAuthHeader(headers http.Header, token string) {
	headers.Del("authorization")
	headers.Del("x-api-key")
	headers.Del("x-goog-api-key")
	headers.Del("cookie")
	setHeaderRaw(headers, "authorization", "Bearer "+token)
}

func (s *GatewayService) buildUpstreamRequestAnthropicOAuthPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, []byte, error) {
	targetURL, err := s.anthropicOAuthPassthroughMessagesURL(account)
	if err != nil {
		return nil, nil, err
	}

	clientBeta := ""
	if c != nil && c.Request != nil {
		clientBeta = getHeaderRaw(c.Request.Header, "anthropic-beta")
	}
	if sanitized, changed := sanitizeAnthropicBodyForBetaTokens(body, clientBeta); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	if c != nil && c.Request != nil {
		copyAnthropicPassthroughHeaders(req.Header, c.Request.Header, s.anthropicPassthroughAllowTimeoutHeaders())
	}

	setAnthropicOAuthPassthroughAuthHeader(req.Header, token)

	if getHeaderRaw(req.Header, "content-type") == "" {
		setHeaderRaw(req.Header, "content-type", "application/json")
	}
	if getHeaderRaw(req.Header, "anthropic-version") == "" {
		setHeaderRaw(req.Header, "anthropic-version", "2023-06-01")
	}
	tkEnsureClaudeCodeSessionHeader(req.Header, body, c)
	setKiroInternalThinkingMirrorHopHeaderForAccount(req.Header, account)

	return req, body, nil
}

func (s *GatewayService) buildCountTokensRequestAnthropicOAuthPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, error) {
	targetURL, err := s.anthropicOAuthPassthroughCountTokensURL(account)
	if err != nil {
		return nil, err
	}

	clientBeta := ""
	if c != nil && c.Request != nil {
		clientBeta = getHeaderRaw(c.Request.Header, "anthropic-beta")
	}
	if sanitized, changed := sanitizeAnthropicBodyForBetaTokens(body, clientBeta); changed {
		body = sanitized
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if c != nil && c.Request != nil {
		copyAnthropicPassthroughHeaders(req.Header, c.Request.Header, s.anthropicPassthroughAllowTimeoutHeaders())
	}

	setAnthropicOAuthPassthroughAuthHeader(req.Header, token)

	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	setKiroInternalThinkingMirrorHopHeaderForAccount(req.Header, account)

	return req, nil
}
