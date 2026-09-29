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
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// handleCodexDirectImagesNonStreamingMulti fulfills n>1 for Codex Direct Images.
// ChatGPT's /images/generations OAuth endpoint silently returns a single image even
// when n>1 is present on the wire (prod 2026-09-29). TokenKey therefore issues one
// upstream call per requested image and merges data[] so the public Images contract
// (n images) remains honest for universal / OAuth traffic.
func (s *OpenAIGatewayService) handleCodexDirectImagesNonStreamingMulti(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	token string,
	proxyURL string,
	firstResp *http.Response,
	upstreamModel string,
	targetURL string,
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
		if shouldClassifyOpenAIUpstreamStreamReadError(err) {
			err = newOpenAIUpstreamStreamReadError(err)
		}
		return OpenAIUsage{}, 0, nil, err
	}
	results, err := parseCodexDirectImagesResponse(firstBody)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	usage, _ := codexDirectImagesUsage(firstBody)
	if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
		observer.Observe(gjson.GetBytes(firstBody, "model").String(), true)
		for _, result := range results {
			observer.Observe(result.Model, true)
		}
	}

	single := *parsed
	single.N = 1
	for len(results) < want {
		extraBody, extraURL, buildErr := buildOpenAIImagesOAuthPayload(&single, upstreamModel)
		if buildErr != nil {
			return OpenAIUsage{}, 0, nil, buildErr
		}
		if extraURL == "" {
			extraURL = targetURL
		}
		extraUsage, extraResults, fetchErr := s.fetchCodexDirectImagesOnce(ctx, c, account, token, proxyURL, extraBody, extraURL, parsed.StickySessionSeed())
		if fetchErr != nil {
			return OpenAIUsage{}, 0, nil, fetchErr
		}
		results = append(results, extraResults...)
		usage = mergeOpenAIImagesUsage(usage, extraUsage)
	}
	if len(results) > want {
		results = results[:want]
	}

	if err := applyOpenAIImagesClientFidelityPostprocess(results, parsed); err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	reconcileOpenAIResponsesImageResultSizes(results, nil)

	body, err := buildCodexDirectMergedImagesBody(firstBody, results, parsed, usage)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), firstResp.Header, s.responseHeaderFilter)
	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := strings.TrimSpace(firstResp.Header.Get("Content-Type")); upstreamType != "" {
			contentType = upstreamType
		}
	}
	c.Data(firstResp.StatusCode, contentType, body)
	return usage, len(results), openAIResponsesImageResultSizes(results), nil
}

func (s *OpenAIGatewayService) fetchCodexDirectImagesOnce(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	token string,
	proxyURL string,
	body []byte,
	targetURL string,
	stickySeed string,
) (OpenAIUsage, []openAIResponsesImageResult, error) {
	upstreamCtx := withOpenAIImagesSelfBuiltRequest(ctx)
	upstreamReq, err := s.buildUpstreamRequest(upstreamCtx, c, account, body, token, true, stickySeed, false)
	if err != nil {
		return OpenAIUsage{}, nil, err
	}
	upstreamReq.URL, err = url.Parse(targetURL)
	if err != nil {
		return OpenAIUsage{}, nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Del("OpenAI-Beta")

	upstreamStart := time.Now()
	resp, err := s.doOpenAIUpstream(upstreamReq, proxyURL, account)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		return OpenAIUsage{}, nil, fmt.Errorf("upstream request failed: %s", sanitizeUpstreamErrorMessage(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		msg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return OpenAIUsage{}, nil, &OpenAIImagesUpstreamError{
			StatusCode: resp.StatusCode,
			ErrorType:  "upstream_error",
			Message:    msg,
		}
	}
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return OpenAIUsage{}, nil, err
	}
	results, err := parseCodexDirectImagesResponse(respBody)
	if err != nil {
		return OpenAIUsage{}, nil, err
	}
	usage, _ := codexDirectImagesUsage(respBody)
	if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
		observer.Observe(gjson.GetBytes(respBody, "model").String(), true)
		for _, result := range results {
			observer.Observe(result.Model, true)
		}
	}
	return usage, results, nil
}

func mergeOpenAIImagesUsage(base, extra OpenAIUsage) OpenAIUsage {
	base.InputTokens += extra.InputTokens
	base.OutputTokens += extra.OutputTokens
	base.CacheReadInputTokens += extra.CacheReadInputTokens
	base.ImageInputTokens += extra.ImageInputTokens
	base.ImageOutputTokens += extra.ImageOutputTokens
	base.ImageCacheReadTokens += extra.ImageCacheReadTokens
	return base
}

func buildCodexDirectMergedImagesBody(template []byte, results []openAIResponsesImageResult, parsed *OpenAIImagesRequest, usage OpenAIUsage) ([]byte, error) {
	out := []byte(`{"created":0,"data":[]}`)
	if created := gjson.GetBytes(template, "created").Int(); created > 0 {
		out, _ = sjson.SetBytes(out, "created", created)
	} else {
		out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
	}
	clientModel := ""
	if parsed != nil {
		clientModel = strings.TrimSpace(parsed.Model)
	}
	for _, img := range results {
		item := []byte(`{}`)
		item, _ = sjson.SetBytes(item, "b64_json", img.Result)
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		if img.Size != "" {
			item, _ = sjson.SetBytes(item, "size", img.Size)
		}
		if img.OutputFormat != "" {
			item, _ = sjson.SetBytes(item, "output_format", img.OutputFormat)
		}
		if img.Quality != "" {
			item, _ = sjson.SetBytes(item, "quality", img.Quality)
		}
		if img.Background != "" {
			item, _ = sjson.SetBytes(item, "background", img.Background)
		}
		model := clientModel
		if model == "" {
			model = img.Model
		}
		if model != "" {
			item, _ = sjson.SetBytes(item, "model", model)
		}
		if parsed != nil && parsed.ResponseFormat == "url" {
			format := img.OutputFormat
			if format == "" && parsed.ExplicitOutputFormat {
				format = parsed.OutputFormat
			}
			item, _ = sjson.SetBytes(item, "url", "data:"+openAIImageOutputMIMEType(format)+";base64,"+img.Result)
			item, _ = sjson.DeleteBytes(item, "b64_json")
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	if len(results) > 0 {
		first := results[0]
		if first.Background != "" {
			out, _ = sjson.SetBytes(out, "background", first.Background)
		}
		if first.OutputFormat != "" {
			out, _ = sjson.SetBytes(out, "output_format", first.OutputFormat)
		} else if parsed != nil && parsed.ExplicitOutputFormat {
			out, _ = sjson.SetBytes(out, "output_format", strings.ToLower(strings.TrimSpace(parsed.OutputFormat)))
		}
		if first.Quality != "" {
			out, _ = sjson.SetBytes(out, "quality", first.Quality)
		}
		if first.Size != "" {
			out, _ = sjson.SetBytes(out, "size", first.Size)
		}
	}
	// Prefer merged usage across all per-image fetches so client-visible usage
	// matches billing; fall back to the first upstream template only when empty.
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageInputTokens > 0 || usage.ImageOutputTokens > 0 {
		out, _ = sjson.SetBytes(out, "usage.input_tokens", usage.InputTokens)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", usage.OutputTokens)
		if usage.ImageInputTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.input_tokens_details.image_tokens", usage.ImageInputTokens)
		}
		if usage.ImageOutputTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.output_tokens_details.image_tokens", usage.ImageOutputTokens)
		}
	} else if usageRaw := gjson.GetBytes(template, "usage").Raw; usageRaw != "" && gjson.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usageRaw))
	}
	return out, nil
}
