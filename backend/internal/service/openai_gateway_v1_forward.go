package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
	switch account.Type {
	case AccountTypeAPIKey:
		raw := strings.TrimSpace(account.GetOpenAIBaseURL())
		if raw == "" {
			return buildOpenAIV1SegmentURL("", segment), nil
		}
		validated, err := s.validateUpstreamBaseURL(raw)
		if err != nil {
			return "", err
		}
		return buildOpenAIV1SegmentURL(validated, segment), nil
	case AccountTypeOAuth:
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
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	forwardBody := body
	if upstreamModel != originalModel {
		forwardBody = s.ReplaceModelInBody(body, upstreamModel)
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
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
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
			if s.rateLimitService != nil {
				s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
			}
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && (isPoolModeRetryableStatus(resp.StatusCode) || isOpenAITransientProcessingError(resp.StatusCode, upstreamMsg, respBody)),
			}
		}
		return s.handleErrorResponse(ctx, resp, c, account, body)
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

	if account.Type == AccountTypeOAuth {
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
