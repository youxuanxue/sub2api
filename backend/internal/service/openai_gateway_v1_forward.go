package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIPlatformV1Base = "https://api.openai.com/v1"

// buildOpenAIV1SegmentURL resolves a base URL (from account or default) plus a
// relative API segment such as "embeddings" or "images/generations".
func buildOpenAIV1SegmentURL(base string, segment string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return openAIPlatformV1Base + "/" + segment
	}
	normalized := strings.TrimRight(base, "/")
	if strings.HasSuffix(normalized, "/responses") {
		normalized = strings.TrimSuffix(normalized, "/responses")
		normalized = strings.TrimRight(normalized, "/")
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/" + segment
	}
	return normalized + "/v1/" + segment
}

func (s *OpenAIGatewayService) buildOpenAIV1TargetURL(account *Account, segment string) (string, error) {
	if account == nil {
		return "", fmt.Errorf("account is required")
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		switch segment {
		case "embeddings", "embeddings/multimodal", "images/generations":
			return newapiintegration.VolcEngineAgentPlanBaseURL + "/" + segment, nil
		default:
			return "", fmt.Errorf("unsupported Agent Plan media endpoint %q", segment)
		}
	}
	switch account.Type {
	case AccountTypeAPIKey:
		raw := strings.TrimSpace(account.GetOpenAIBaseURL())
		if raw == "" {
			if account.IsGrokAPIKey() {
				return "", fmt.Errorf("grok relay account %d missing base_url", account.ID)
			}
			return buildOpenAIV1SegmentURL("", segment), nil
		}
		validated, err := s.validateUpstreamBaseURLForAccount(account, raw)
		if err != nil {
			return "", err
		}
		return buildOpenAIV1SegmentURL(validated, segment), nil
	case AccountTypeOAuth:
		// Grok (seventh platform) OAuth forwards to api.x.ai/v1 (OpenAI-compatible),
		// NOT the ChatGPT platform base. The Bearer is the grok OAuth token resolved
		// by GetAccessToken's grok branch.
		if account.IsGrokOAuth() {
			validated, err := s.validateUpstreamBaseURLForAccount(account, strings.TrimSpace(account.GetGrokBaseURL()))
			if err != nil {
				return "", err
			}
			return buildOpenAIV1SegmentURL(validated, segment), nil
		}
		return buildOpenAIV1SegmentURL(openAIPlatformV1Base, segment), nil
	default:
		return "", fmt.Errorf("unsupported account type: %s", account.Type)
	}
}

// ForwardAsEmbeddings forwards POST /v1/embeddings to the OpenAI-compatible upstream.
func (s *OpenAIGatewayService) ForwardAsEmbeddings(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	return s.forwardOpenAIV1JSON(ctx, c, account, body, defaultMappedModel, "embeddings")
}

// ForwardAsImageGenerations forwards POST /v1/images/generations to the OpenAI-compatible upstream.
func (s *OpenAIGatewayService) ForwardAsImageGenerations(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	return s.forwardOpenAIV1JSON(ctx, c, account, body, defaultMappedModel, "images/generations")
}

func (s *OpenAIGatewayService) forwardOpenAIV1JSON(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
	urlSegment string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	if len(body) == 0 {
		return nil, fmt.Errorf("empty request body")
	}
	originalModel := strings.TrimSpace(gjsonGetModelString(body))
	if originalModel == "" {
		return nil, fmt.Errorf("model is required")
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) && gjson.GetBytes(body, "stream").Bool() {
		writeOpenAIEmbeddingsError(c, http.StatusBadRequest, "invalid_request_error", "Agent Plan media streaming is not supported by this endpoint")
		return nil, fmt.Errorf("agent plan media streaming is unsupported")
	}
	billingModel, upstreamModel := resolveOpenAICompatForwardModels(account, originalModel, defaultMappedModel)
	// Embeddings is a billed surface and must hit the priced-serving gate.
	// images/generations stays on its own media path — do not gate it here.
	if urlSegment == "embeddings" {
		if !s.tkPricedServingGate(ctx, c, tkGateWireOpenAI, account.Platform, billingModel, originalModel) {
			return nil, fmt.Errorf("priced serving gate: model %q not priced for platform %q", billingModel, account.Platform)
		}
	}
	forwardBody := body
	if upstreamModel != originalModel {
		forwardBody = s.ReplaceModelInBody(body, upstreamModel)
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) && urlSegment == "embeddings" &&
		volcEnginePlanMultimodalEmbeddingInput(forwardBody) {
		if encoding := gjson.GetBytes(body, "encoding_format").String(); encoding != "" && encoding != "float" {
			writeOpenAIEmbeddingsError(c, http.StatusBadRequest, "invalid_request_error", "Agent Plan multimodal embeddings require float encoding")
			return nil, fmt.Errorf("unsupported multimodal embedding encoding")
		}
		urlSegment = "embeddings/multimodal"
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	targetURL, err := s.buildOpenAIV1TargetURL(account, urlSegment)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(forwardBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		return nil, candidateTransportFailure(ctx, fmt.Errorf("upstream request failed: %s", safeErr), err)
	}
	upstreamBody := resp.Body
	defer func() { _ = upstreamBody.Close() }()

	var respBody []byte
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		respBody, err = ReadUpstreamResponseBody(upstreamBody, s.cfg, c, openAITooLargeError)
	} else {
		respBody, err = io.ReadAll(io.LimitReader(upstreamBody, 2<<20))
	}
	if err != nil {
		return nil, fmt.Errorf("read upstream response body: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	if resp.StatusCode >= 400 {
		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		if s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody) {
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
			return nil, &UpstreamFailoverError{
				StatusCode:   resp.StatusCode,
				ResponseBody: respBody,
				// TK: 用 account 级判定（含 TK 默认 503/529 + per-account 覆写）而非
				// 裸 pkg 默认，与其它 pool 路径一致（见 account_tk_pool_retry.go）。
				RetryableOnSameAccount: tkOpenAICompatRetryableOnSameAccount(account, resp.StatusCode, upstreamMsg, respBody, true),
			}
		}
		return s.handleErrorResponse(ctx, resp, c, account, body)
	}

	if isNewAPIVolcEngineAgentPlanAccount(account) {
		responseBody := respBody
		if !gjson.ValidBytes(responseBody) {
			return nil, fmt.Errorf("invalid Agent Plan media response")
		}
		if urlSegment == "embeddings/multimodal" {
			responseBody, err = normalizeVolcEnginePlanEmbeddingResponse(responseBody)
			if err != nil {
				return nil, err
			}
		}
		if urlSegment == "images/generations" {
			if countOpenAIResponseImageOutputsFromJSONBytes(responseBody) == 0 {
				return nil, fmt.Errorf("agent plan image response contains no images")
			}
		} else if !gjson.GetBytes(responseBody, "data.0.embedding").Exists() || extractOpenAIEmbeddingsUsage(responseBody).InputTokens <= 0 {
			return nil, fmt.Errorf("agent plan embedding response missing vector or usage")
		}
		resp.Body = io.NopCloser(bytes.NewReader(responseBody))
		resp.ContentLength = int64(len(responseBody))
		resp.Header.Del("Content-Length")
	}
	usage, err := s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, billingModel)
	if err != nil {
		return nil, err
	}
	if usage == nil {
		usage = &openaiNonStreamingResult{OpenAIUsage: &OpenAIUsage{}}
	}
	var openAIUsage OpenAIUsage
	if usage.OpenAIUsage != nil {
		openAIUsage = *usage.OpenAIUsage
	}

	if account.IsOpenAIOAuth() {
		if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
			s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
		}
	}

	return &OpenAIForwardResult{
		RequestID:     resp.Header.Get("x-request-id"),
		Usage:         openAIUsage,
		ImageCount:    usage.imageCount,
		Model:         originalModel,
		BillingModel:  billingModel,
		UpstreamModel: upstreamModel,
		Stream:        false,
		Duration:      time.Since(startTime),
	}, nil
}

func gjsonGetModelString(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}
