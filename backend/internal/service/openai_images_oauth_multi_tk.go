package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
)

// handleOpenAIImagesOAuthNonStreamingMulti fulfills n>1 for OAuth Responses
// image_generation. ChatGPT Codex rejects tools[].n (unknown_parameter); TokenKey
// therefore issues one Responses call per requested image and merges data[].
func (s *OpenAIGatewayService) handleOpenAIImagesOAuthNonStreamingMulti(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	token string,
	proxyURL string,
	firstResp *http.Response,
	upstreamModel string,
	requestModel string,
) (OpenAIUsage, int, []string, error) {
	want := parsed.N
	if want < 1 {
		want = 1
	}
	if want > 4 {
		want = 4
	}

	firstBody, err := ReadUpstreamResponseBody(firstResp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if shouldClassifyOpenAIUpstreamStreamReadError(err, c.Request.Context()) {
			err = newOpenAIUpstreamStreamReadError(err)
		}
		return OpenAIUsage{}, 0, nil, err
	}
	observeOpenAIResponsesSSEBody(c, string(firstBody))

	usage := OpenAIUsage{}
	forEachOpenAISSEDataPayload(string(firstBody), func(data []byte) {
		s.parseOpenAIImagesSSEUsageBytes(data, &usage)
	})
	results, createdAt, usageRaw, firstMeta, _, err := collectOpenAIImagesFromResponsesBody(firstBody)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	if len(results) == 0 {
		if upstreamErr := extractOpenAIImagesUpstreamError(firstBody); upstreamErr != nil {
			setOpsUpstreamError(c, upstreamErr.clientStatusCode(), upstreamErr.clientMessage(), "")
			if !IsOpenAIImagesRetryableUpstreamError(upstreamErr) {
				writeOpenAIImagesUpstreamErrorResponse(c, upstreamErr)
			}
			return OpenAIUsage{}, 0, nil, upstreamErr
		}
		if textFallbackErr := openAIImagesTextFallbackError(firstBody); textFallbackErr != nil {
			setOpsUpstreamError(c, textFallbackErr.clientStatusCode(), textFallbackErr.clientMessage(), summarizeOpenAIImagesNoOutputBody(firstBody))
			if !IsOpenAIImagesRetryableUpstreamError(textFallbackErr) {
				writeOpenAIImagesUpstreamErrorResponse(c, textFallbackErr)
			}
			return OpenAIUsage{}, 0, nil, textFallbackErr
		}
		setOpsUpstreamError(c, http.StatusBadGateway, "upstream did not return image output", summarizeOpenAIImagesNoOutputBody(firstBody))
		return OpenAIUsage{}, 0, nil, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			ResponseBody:           firstBody,
			RetryableOnSameAccount: true,
		}
	}

	single := *parsed
	single.N = 1
	for len(results) < want {
		extraUsage, extraResults, extraMeta, fetchErr := s.fetchOpenAIImagesResponsesOnce(ctx, c, account, &single, token, proxyURL, upstreamModel)
		if fetchErr != nil {
			return OpenAIUsage{}, 0, nil, fetchErr
		}
		if len(extraResults) == 0 {
			return OpenAIUsage{}, 0, nil, &OpenAIImagesUpstreamError{
				StatusCode: http.StatusBadGateway,
				ErrorType:  "upstream_error",
				Message:    "upstream did not return image output on multi-fetch",
			}
		}
		results = append(results, extraResults...)
		usage = mergeOpenAIImagesUsage(usage, extraUsage)
		if strings.TrimSpace(firstMeta.Model) == "" && strings.TrimSpace(extraMeta.Model) != "" {
			firstMeta.Model = extraMeta.Model
		}
	}
	if len(results) > want {
		results = results[:want]
	}

	if err := applyOpenAIImagesClientFidelityPostprocess(results, parsed); err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	reconcileOpenAIResponsesImageResultSizes(results, &firstMeta)
	if strings.TrimSpace(firstMeta.Model) == "" {
		firstMeta.Model = strings.TrimSpace(requestModel)
	}
	if len(results) > 0 && results[0].OutputFormat != "" {
		firstMeta.OutputFormat = results[0].OutputFormat
	}

	responseFormat := ""
	if parsed != nil {
		responseFormat = parsed.ResponseFormat
	}
	responseBody, err := buildOpenAIImagesAPIResponse(results, createdAt, usageRaw, firstMeta, responseFormat)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), firstResp.Header, s.responseHeaderFilter)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	responseBody = s.tkMaybeOffloadImagesToS3(c.Request.Context(), responseBody, responseFormat)
	c.Data(firstResp.StatusCode, "application/json; charset=utf-8", responseBody)
	return usage, len(results), openAIResponsesImageResultSizes(results), nil
}

func (s *OpenAIGatewayService) fetchOpenAIImagesResponsesOnce(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	token string,
	proxyURL string,
	upstreamModel string,
) (OpenAIUsage, []openAIResponsesImageResult, openAIResponsesImageResult, error) {
	body, err := buildOpenAIImagesResponsesRequest(parsed, upstreamModel)
	if err != nil {
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, err
	}
	upstreamCtx := withOpenAIImagesSelfBuiltRequest(ctx)
	upstreamReq, err := s.buildUpstreamRequest(upstreamCtx, c, account, body, token, true, parsed.StickySessionSeed(), false)
	if err != nil {
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, err
	}
	upstreamReq.URL, err = url.Parse(chatgptCodexURL)
	if err != nil {
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Del("OpenAI-Beta")

	upstreamStart := time.Now()
	resp, err := s.doOpenAIUpstream(upstreamReq, proxyURL, account)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, fmt.Errorf("upstream request failed: %s", sanitizeUpstreamErrorMessage(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		msg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, &OpenAIImagesUpstreamError{
			StatusCode: resp.StatusCode,
			ErrorType:  "upstream_error",
			Message:    msg,
		}
	}
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, err
	}
	observeOpenAIResponsesSSEBody(c, string(respBody))
	usage := OpenAIUsage{}
	forEachOpenAISSEDataPayload(string(respBody), func(data []byte) {
		s.parseOpenAIImagesSSEUsageBytes(data, &usage)
	})
	results, _, _, meta, _, err := collectOpenAIImagesFromResponsesBody(respBody)
	if err != nil {
		return OpenAIUsage{}, nil, openAIResponsesImageResult{}, err
	}
	return usage, results, meta, nil
}
