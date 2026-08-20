package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
	"go.uber.org/zap"
)

const (
	openAIResponsesInputItemTokenOverhead = 3
	openAIResponsesContentPartOverhead    = 1
)

type openAIInputTokensCountRequest struct {
	Model        string                    `json:"model"`
	Instructions string                    `json:"instructions,omitempty"`
	Input        json.RawMessage           `json:"input,omitempty"`
	Tools        []apicompat.ResponsesTool `json:"tools,omitempty"`
	ToolChoice   json.RawMessage           `json:"tool_choice,omitempty"`
}

type openAIInputTokensCountPrepared struct {
	Request         openAIInputTokensCountRequest
	OriginalModel   string
	NormalizedModel string
	BillingModel    string
	UpstreamModel   string
}

// ForwardResponsesInputTokens handles the native OpenAI
// POST /v1/responses/input_tokens shape. Custom OpenAI-compatible relays often
// implement /responses but not this preflight endpoint, so those accounts use
// the local estimator instead of receiving a request that is known to fail.
func (s *OpenAIGatewayService) ForwardResponsesInputTokens(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) error {
	if account == nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI accounts")
		return fmt.Errorf("responses input_tokens: missing account")
	}

	prepared, err := prepareNativeOpenAIInputTokensCountRequest(body, account)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}

	if shouldEstimateOpenAIInputTokensLocally(account) {
		writeOpenAIResponsesInputTokensFallback(c, account, prepared, 0, "custom_relay")
		return nil
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("responses input_tokens: get access token: %w", err)
	}

	upstreamBody := ReplaceModelInBody(body, prepared.UpstreamModel)
	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, account, upstreamBody, token)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("responses input_tokens: build upstream request: %w", err)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("responses input_tokens: upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		return fmt.Errorf("responses input_tokens: read upstream response: %w", err)
	}

	if resp.StatusCode >= 400 {
		if isOpenAIResponsesInputTokensUnsupported(account, resp.StatusCode, respBody) {
			writeOpenAIResponsesInputTokensFallback(c, account, prepared, resp.StatusCode, "upstream_unsupported")
			return nil
		}
		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		}
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, "")
		writeOpenAIResponsesInputTokensError(c, resp.StatusCode, "upstream_error", "Upstream request failed")
		if upstreamMsg == "" {
			return fmt.Errorf("responses input_tokens: upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("responses input_tokens: upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	inputTokens := gjson.GetBytes(respBody, "input_tokens")
	if !inputTokens.Exists() {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream response missing input_tokens")
		return fmt.Errorf("responses input_tokens: upstream response missing input_tokens")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(http.StatusOK, contentType, respBody)
	return nil
}

func prepareNativeOpenAIInputTokensCountRequest(body []byte, account *Account) (*openAIInputTokensCountPrepared, error) {
	var req openAIInputTokensCountRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse responses input_tokens request: %w", err)
	}
	originalModel := strings.TrimSpace(req.Model)
	if originalModel == "" {
		return nil, fmt.Errorf("parse responses input_tokens request: model is required")
	}
	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	req.Model = upstreamModel
	return &openAIInputTokensCountPrepared{
		Request:         req,
		OriginalModel:   originalModel,
		NormalizedModel: originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

func shouldEstimateOpenAIInputTokensLocally(account *Account) bool {
	if account == nil || account.IsGrok() || account.IsCNProvider() || account.Type == AccountTypeUpstream {
		return true
	}
	if account.Type != AccountTypeAPIKey {
		return false
	}
	rawBaseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if rawBaseURL == "" {
		return false
	}
	parsed, err := url.Parse(rawBaseURL)
	if err != nil {
		return true
	}
	return !strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func isOpenAIResponsesInputTokensUnsupported(account *Account, statusCode int, body []byte) bool {
	if statusCode == http.StatusNotFound {
		return true
	}
	return account != nil && account.Type == AccountTypeOAuth && isOpenAIOAuthInputTokensUnsupported(statusCode, body)
}

func writeOpenAIResponsesInputTokensFallback(c *gin.Context, account *Account, prepared *openAIInputTokensCountPrepared, statusCode int, reason string) {
	estimated := openAIInputTokensFallbackMinimum
	if prepared != nil {
		if got, err := estimateOpenAIInputTokens(prepared.Request); err == nil && got > 0 {
			estimated = got
		}
	}
	accountID := int64(0)
	upstreamModel := ""
	if account != nil {
		accountID = account.ID
	}
	if prepared != nil {
		upstreamModel = prepared.UpstreamModel
	}
	logger.L().Info("openai responses input_tokens: local estimate fallback",
		zap.Int64("account_id", accountID),
		zap.Int("upstream_status", statusCode),
		zap.Int("estimated_input_tokens", estimated),
		zap.String("upstream_model", upstreamModel),
		zap.String("reason", reason),
	)
	c.JSON(http.StatusOK, gin.H{
		"object":       "response.input_tokens",
		"input_tokens": estimated,
	})
}

func writeOpenAIResponsesInputTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// EstimateGrokCountTokens estimates an Anthropic-compatible count_tokens request
// without calling upstream when the selected account is Grok.
func EstimateGrokCountTokens(body []byte) (int, error) {
	return estimateAnthropicCountTokensLocally(body)
}

// estimateAnthropicCountTokensLocally 走 Anthropic→Responses→tiktoken 链本地估算
// count_tokens，不发任何上游请求（上游无兼容端点的平台使用）。
func estimateAnthropicCountTokensLocally(body []byte) (int, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return 0, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}
	if strings.TrimSpace(anthropicReq.Model) == "" {
		return 0, fmt.Errorf("parse anthropic count_tokens request: model is required")
	}

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return 0, fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	estimated, err := estimateOpenAIInputTokens(openAIInputTokensCountRequest{
		Model:        anthropicReq.Model,
		Instructions: responsesReq.Instructions,
		Input:        responsesReq.Input,
		Tools:        responsesReq.Tools,
		ToolChoice:   responsesReq.ToolChoice,
	})
	if err != nil {
		return 0, fmt.Errorf("estimate input tokens: %w", err)
	}
	if estimated < openAIInputTokensFallbackMinimum {
		estimated = openAIInputTokensFallbackMinimum
	}
	return estimated, nil
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

	// 国产供应商（全部协议，含 anthropic）：一律本地估算，不发上游请求。
	// 依据（2026-08 核实）：三家的 Anthropic 兼容层均未提供
	// /v1/messages/count_tokens——DeepSeek 官方 anthropic_api 文档无此端点
	// （且注明 anthropic-version 头被忽略），聚合网关 OpenModel 明确标注
	// count_tokens 为 "Anthropic only"，Kimi/智谱亦无任何文档承诺。转发上游
	// 只会常态 404，且错误还会流入账号处置逻辑误伤整账号调度；Claude Code
	// 高频调用此端点，本地 tiktoken 估算是与 Grok 一致的既有方案。
	if account.IsCNProvider() {
		estimated, err := estimateAnthropicCountTokensLocally(body)
		if err != nil {
			writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
			return fmt.Errorf("count_tokens: estimate cn provider input tokens: %w", err)
		}
		logger.L().Debug("openai count_tokens: cn provider local estimate",
			zap.Int64("account_id", account.ID),
			zap.Int("estimated_input_tokens", estimated),
		)
		c.JSON(http.StatusOK, gin.H{
			"input_tokens": estimated,
		})
		return nil
	}

	prepared, err := prepareOpenAIInputTokensCountRequest(body, account, defaultMappedModel)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}

	upstreamBody, err := marshalOpenAIUpstreamJSON(prepared.Request)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("marshal openai input_tokens body: %w", err)
	}

	logger.L().Debug("openai count_tokens: model mapping applied",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", prepared.OriginalModel),
		zap.String("normalized_model", prepared.NormalizedModel),
		zap.String("billing_model", prepared.BillingModel),
		zap.String("upstream_model", prepared.UpstreamModel),
	)

	token, _, err := s.getInputTokensAuthToken(ctx, account)
	if err != nil {
		if shouldEstimateOpenAIInputTokensForAuthError(account, err) {
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
		fallback := classifyOpenAIInputTokensFallback(account, resp.StatusCode, respBody)
		switch fallback.Kind {
		case openAIInputTokensFallbackPreparedEstimate:
			writeOpenAIPreparedInputTokensFallback(c, account, prepared, body, resp.StatusCode)
			return nil
		case openAIInputTokensFallbackAnthropicEstimate:
			writeEstimatedAnthropicCountTokens(c, body)
			return nil
		}

		upstreamMsg := fallback.UpstreamMessage
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

func prepareOpenAIInputTokensCountRequest(
	body []byte,
	account *Account,
	defaultMappedModel string,
) (*openAIInputTokensCountPrepared, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}

	originalModel := anthropicReq.Model
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	billingModel := resolveOpenAIForwardModel(account, normalizedModel, strings.TrimSpace(defaultMappedModel))
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	return &openAIInputTokensCountPrepared{
		Request: openAIInputTokensCountRequest{
			Model:        upstreamModel,
			Instructions: responsesReq.Instructions,
			Input:        responsesReq.Input,
			Tools:        responsesReq.Tools,
			ToolChoice:   responsesReq.ToolChoice,
		},
		OriginalModel:   originalModel,
		NormalizedModel: normalizedModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

func (s *OpenAIGatewayService) getInputTokensAuthToken(ctx context.Context, account *Account) (string, string, error) {
	if account == nil {
		return "", "", fmt.Errorf("count_tokens: missing account")
	}
	if account.Platform == PlatformGrok {
		token, err := s.grokResponsesAuthToken(ctx, nil, account)
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
		if baseURL := nativeOpenAIBaseURLForAccount(account); strings.TrimSpace(baseURL) != "" {
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
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
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

	// 账号级请求头覆写（仅 openai api_key 账号启用时生效；OAuth 路径 no-op）
	account.ApplyHeaderOverrides(req.Header)

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

func estimateOpenAIInputTokens(req openAIInputTokensCountRequest) (int, error) {
	codec, err := openAIInputTokensCodecForModel(req.Model)
	if err != nil {
		return 0, err
	}

	total := 0
	addCount := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	if err := addCount(req.Instructions); err != nil {
		return 0, err
	}
	inputTokens, err := estimateOpenAIInputTokensForInput(codec, req.Input)
	if err != nil {
		return 0, err
	}
	total += inputTokens

	for _, tool := range req.Tools {
		raw, err := marshalOpenAIUpstreamJSON(tool)
		if err != nil {
			return 0, err
		}
		if err := addCount(string(raw)); err != nil {
			return 0, err
		}
	}
	if len(req.ToolChoice) > 0 {
		compacted, err := compactOpenAIInputTokensJSON(req.ToolChoice)
		if err != nil {
			return 0, err
		}
		if err := addCount(compacted); err != nil {
			return 0, err
		}
	}

	if total < 0 {
		return 0, nil
	}
	return total, nil
}

func estimateOpenAIInputTokensForInput(codec tokenizer.Codec, raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}

	var plainText string
	if err := json.Unmarshal(raw, &plainText); err == nil {
		return codec.Count(plainText)
	}

	var items []apicompat.ResponsesInputItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return estimateOpenAIInputTokensForInputItems(codec, items)
	}

	compacted, err := compactOpenAIInputTokensJSON(raw)
	if err != nil {
		return 0, err
	}
	return codec.Count(compacted)
}

func estimateOpenAIInputTokensForInputItems(codec tokenizer.Codec, items []apicompat.ResponsesInputItem) (int, error) {
	total := 0
	countText := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	for _, item := range items {
		total += openAIResponsesInputItemTokenOverhead
		if err := countText(item.Role); err != nil {
			return 0, err
		}
		if item.Type != "" && item.Type != "message" {
			if err := countText(item.Type); err != nil {
				return 0, err
			}
		}
		if err := countText(item.Name); err != nil {
			return 0, err
		}
		if err := countText(item.Arguments); err != nil {
			return 0, err
		}
		if err := countText(item.Output); err != nil {
			return 0, err
		}
		if err := countText(item.CallID); err != nil {
			return 0, err
		}
		if err := countText(item.ID); err != nil {
			return 0, err
		}

		if len(bytes.TrimSpace(item.Content)) == 0 {
			continue
		}

		var contentText string
		if err := json.Unmarshal(item.Content, &contentText); err == nil {
			if err := countText(contentText); err != nil {
				return 0, err
			}
			continue
		}

		var parts []apicompat.ResponsesContentPart
		if err := json.Unmarshal(item.Content, &parts); err == nil {
			for _, part := range parts {
				total += openAIResponsesContentPartOverhead
				switch part.Type {
				case "input_text", "output_text", "text":
					if err := countText(part.Text); err != nil {
						return 0, err
					}
				case "input_image":
					if err := countText(estimateOpenAIInputImageText(part.ImageURL)); err != nil {
						return 0, err
					}
				default:
					if err := countText(part.Type); err != nil {
						return 0, err
					}
				}
			}
			continue
		}

		compacted, err := compactOpenAIInputTokensJSON(item.Content)
		if err != nil {
			return 0, err
		}
		if err := countText(compacted); err != nil {
			return 0, err
		}
	}

	return total, nil
}

func estimateOpenAIInputImageText(imageURL string) string {
	trimmed := strings.TrimSpace(imageURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		if comma := strings.Index(trimmed, ","); comma > 0 {
			return trimmed[:comma]
		}
	}
	return trimmed
}

func compactOpenAIInputTokensJSON(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func openAIInputTokensCodecForModel(model string) (tokenizer.Codec, error) {
	switch openAIInputTokensEncodingForModel(model) {
	case tokenizer.Cl100kBase:
		return tokenizer.Get(tokenizer.Cl100kBase)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

func openAIInputTokensEncodingForModel(model string) tokenizer.Encoding {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gpt-3.5"),
		(strings.HasPrefix(normalized, "gpt-4") &&
			!strings.HasPrefix(normalized, "gpt-4o") &&
			!strings.HasPrefix(normalized, "gpt-4.1")),
		strings.HasPrefix(normalized, "text-embedding-"):
		return tokenizer.Cl100kBase
	default:
		return tokenizer.O200kBase
	}
}
