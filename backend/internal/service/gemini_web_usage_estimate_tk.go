package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/integration/geminiweb"
	"github.com/gin-gonic/gin"
)

// Context keys for Gemini Web local usage estimation. Handlers stash the
// upstream GenerateContentResponse (or accumulated stream text) so the shared
// ForwardResult exit can settle when Worker omits usageMetadata.
const (
	ctxKeyGeminiWebEstimateResponseBody = "tk_gemini_web_estimate_response_body"
	ctxKeyGeminiWebEstimateOutputText   = "tk_gemini_web_estimate_output_text"
)

func stashGeminiWebEstimateResponseBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	c.Set(ctxKeyGeminiWebEstimateResponseBody, append([]byte(nil), body...))
}

func stashGeminiWebEstimateOutputText(c *gin.Context, text string) {
	if c == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.Set(ctxKeyGeminiWebEstimateOutputText, text)
}

func geminiWebEstimateResponseBody(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(ctxKeyGeminiWebEstimateResponseBody); ok {
		if b, ok := v.([]byte); ok {
			return b
		}
	}
	return nil
}

func geminiWebEstimateOutputText(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(ctxKeyGeminiWebEstimateOutputText); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func geminiUsageHasBillableTokens(u ClaudeUsage) bool {
	return u.InputTokens > 0 ||
		u.OutputTokens > 0 ||
		u.CacheReadInputTokens > 0 ||
		u.CacheCreationInputTokens > 0 ||
		u.CacheCreation5mTokens > 0 ||
		u.CacheCreation1hTokens > 0 ||
		u.ImageOutputTokens > 0
}

// applyGeminiWebTextUsageEstimate fills empty text usage for Gemini Web Worker
// accounts from the shared tokenestimate stack (Cursor/Kiro). Reported
// usageMetadata always wins; image settlements (ImageCount > 0) are untouched;
// countTokens is skipped by callers.
func applyGeminiWebTextUsageEstimate(c *gin.Context, account *Account, result *ForwardResult, requestBody []byte) {
	if result == nil || !isGeminiWebAccount(account) {
		return
	}
	if result.ImageCount > 0 {
		return
	}
	if geminiUsageHasBillableTokens(result.Usage) {
		return
	}

	estimated := geminiweb.EstimateUsage(requestBody, geminiWebEstimateResponseBody(c), geminiWebEstimateOutputText(c))
	if estimated.Input <= 0 && estimated.Output <= 0 {
		return
	}
	result.Usage = ClaudeUsage{
		InputTokens:  estimated.Input,
		OutputTokens: estimated.Output,
	}
	result.BillingTier = geminiweb.EstimatedBillingTier
}
