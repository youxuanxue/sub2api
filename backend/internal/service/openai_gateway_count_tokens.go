package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type openAIInputTokensCountRequest struct {
	Model        string                    `json:"model"`
	Instructions string                    `json:"instructions,omitempty"`
	Input        json.RawMessage           `json:"input,omitempty"`
	Tools        []apicompat.ResponsesTool `json:"tools,omitempty"`
	ToolChoice   json.RawMessage           `json:"tool_choice,omitempty"`
}

// ForwardCountTokensAsAnthropic bridges Anthropic /v1/messages/count_tokens to
// OpenAI POST /v1/responses/input_tokens and returns Anthropic-compatible output.
func (s *OpenAIGatewayService) ForwardCountTokensAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) error {
	if account == nil {
		writeAnthropicCountTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI accounts")
		return fmt.Errorf("count_tokens: missing account")
	}

	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}

	originalModel := anthropicReq.Model
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	billingModel := resolveOpenAIForwardModel(account, normalizedModel, strings.TrimSpace(defaultMappedModel))
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to convert request body")
		return fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	upstreamBody, err := marshalOpenAIUpstreamJSON(openAIInputTokensCountRequest{
		Model:        upstreamModel,
		Instructions: responsesReq.Instructions,
		Input:        responsesReq.Input,
		Tools:        responsesReq.Tools,
		ToolChoice:   responsesReq.ToolChoice,
	})
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("marshal openai input_tokens body: %w", err)
	}

	logger.L().Debug("openai count_tokens: model mapping applied",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("normalized_model", normalizedModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
	)

	token, _, err := s.getInputTokensAuthToken(ctx, account)
	if err != nil {
		if isOpenAICompatCountTokensCapabilityGap(account, err) {
			writeEstimatedAnthropicCountTokens(c, body)
			return nil
		}
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("get access token: %w", err)
	}

	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, account, upstreamBody, token)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("build input_tokens request: %w", err)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("openai input_tokens upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		return fmt.Errorf("read input_tokens response: %w", err)
	}

	if resp.StatusCode >= 400 {
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		if account.Type == AccountTypeOAuth && isOpenAIOAuthInputTokensUnsupported(resp.StatusCode, respBody) {
			writeEstimatedAnthropicCountTokens(c, body)
			return nil
		}
		if isOpenAIInputTokensUnsupported(resp.StatusCode, respBody) {
			writeEstimatedAnthropicCountTokens(c, body)
			return nil
		}
		if isOpenAICompatInputTokensCapabilityGap(account, resp.StatusCode, upstreamMsg, respBody) {
			writeEstimatedAnthropicCountTokens(c, body)
			return nil
		}

		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		}

		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = truncateString(string(respBody), maxBytes)
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

		errMsg := "Upstream request failed"
		switch resp.StatusCode {
		case 429:
			errMsg = "Rate limit exceeded"
		case 500, 502, 503, 504, 529:
			errMsg = "Upstream service temporarily unavailable"
		}
		writeAnthropicCountTokensError(c, resp.StatusCode, "upstream_error", errMsg)
		if upstreamMsg == "" {
			return fmt.Errorf("input_tokens upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("input_tokens upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	inputTokens := gjson.GetBytes(respBody, "input_tokens")
	if !inputTokens.Exists() {
		writeEstimatedAnthropicCountTokens(c, body)
		return nil
	}

	c.JSON(http.StatusOK, gin.H{
		"input_tokens": int(inputTokens.Int()),
	})
	return nil
}

func (s *OpenAIGatewayService) getInputTokensAuthToken(ctx context.Context, account *Account) (string, string, error) {
	if account == nil {
		return "", "", fmt.Errorf("count_tokens: missing account")
	}
	if account.Platform == PlatformGrok {
		token, err := s.grokResponsesAuthToken(ctx, account)
		return token, "", err
	}
	return s.GetAccessToken(ctx, account)
}

func (s *OpenAIGatewayService) buildInputTokensUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, error) {
	targetURL := openaiPlatformAPIInputTokensURL
	switch {
	case account.Platform == PlatformGrok:
		grokURL, err := s.resolveGrokInputTokensUpstream(account)
		if err != nil {
			return nil, err
		}
		targetURL = grokURL
	case account.Type == AccountTypeAPIKey:
		if baseURL := account.GetOpenAIBaseURL(); strings.TrimSpace(baseURL) != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesInputTokensURL(validatedURL)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lower := strings.ToLower(strings.TrimSpace(key))
			if lower != "user-agent" && lower != "accept-language" {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}

	return req, nil
}

func writeAnthropicCountTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func isOpenAIInputTokensUnsupported(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	return strings.Contains(msg, "input_tokens") && strings.Contains(msg, "not found")
}

func isOpenAIOAuthInputTokensUnsupported(statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(string(body)))
	}
	return strings.Contains(msg, "input_tokens") &&
		(strings.Contains(msg, "not found") || strings.Contains(msg, "unsupported"))
}

func isOpenAICompatCountTokensCapabilityGap(account *Account, err error) bool {
	if account == nil || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if account.Platform == PlatformNewAPI {
		if account.Type == AccountTypeServiceAccount && account.ChannelType == newapiconstant.ChannelTypeVertexAi {
			return true
		}
		return strings.Contains(msg, "api_key not found")
	}
	return false
}

func isOpenAICompatInputTokensCapabilityGap(account *Account, statusCode int, upstreamMsg string, body []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(string(body)))
	}
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "input_tokens") &&
		(strings.Contains(msg, "missing") || strings.Contains(msg, "not found") || strings.Contains(msg, "unsupported")) {
		return true
	}
	if statusCode == http.StatusNotFound && openAICompatInputTokensBare404CanEstimate(account) && strings.Contains(msg, "not found") {
		return true
	}
	if strings.Contains(msg, "upstream returned 403") &&
		(strings.Contains(msg, "access/policy rejection") || strings.Contains(msg, "rejected by upstream")) {
		return true
	}
	return false
}

func openAICompatInputTokensBare404CanEstimate(account *Account) bool {
	if account == nil || account.Type == AccountTypeOAuth {
		return false
	}
	switch account.Platform {
	case PlatformOpenAI, PlatformNewAPI, PlatformGrok:
		return true
	default:
		return false
	}
}
