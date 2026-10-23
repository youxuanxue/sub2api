package service

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/gin-gonic/gin"
)

// gatewayBodyParseStats counts full-document JSON decodes on one request.
// Tests install it to prove chat admission does not unmarshal the body again
// for Claude Code detection, and that the canonical profile is decoded once.
type gatewayBodyParseStats struct {
	canonical      int
	claudeDocument int
}

type gatewayBodyParseStatsKey struct{}

func noteGatewayCanonicalParse(ctx context.Context) {
	stats, _ := ctx.Value(gatewayBodyParseStatsKey{}).(*gatewayBodyParseStats)
	if stats != nil {
		stats.canonical++
	}
}

func noteGatewayClaudeDocument(ctx context.Context) {
	stats, _ := ctx.Value(gatewayBodyParseStatsKey{}).(*gatewayBodyParseStats)
	if stats != nil {
		stats.claudeDocument++
	}
}

// PrepareCandidateIngress uses the existing client and session parsers before
// billing admission, when sticky-only eligibility already matters.
func (r *UniversalRoutingResolver) PrepareCandidateIngress(c *gin.Context, key *APIKey, shape UniversalShape, path, model string, body []byte, forcedPlatform string) (*CandidateRequest, error) {
	ctx := WithCandidateIdentity(c.Request.Context(), key.UserID, key.ID)
	// Establish the protocolrouter request at the single HTTP candidate ingress.
	// PrepareCandidateRequest reuses this immutable request's profile when the
	// path has not applied a Direct-only model/body rewrite; handlers and
	// converters then observe the same request digest through the context.
	ctx = r.WithRequest(ctx, shape, path, model, body)
	// OpenAI chat (and any non-messages path) never reads this tree. Unmarshal
	// only when Claude Code validation will inspect system/metadata fields.
	var document map[string]any
	if claudeCodeValidationNeedsBody(c.Request) {
		if err := json.Unmarshal(body, &document); err == nil {
			noteGatewayClaudeDocument(ctx)
		}
	}
	validator := NewClaudeCodeValidator()
	ctx = SetClaudeCodeClient(ctx, validator.Validate(c.Request, document))
	ctx = SetClaudeDesktopGatewayClient(ctx, IsClaudeDesktopGatewayUserAgent(c.GetHeader("User-Agent")))
	c.Request = c.Request.WithContext(ctx)
	session := ""
	if shape == ShapeAnthropicMessages || shape == ShapeAnthropicCountTokens || shape == ShapeGemini {
		platform := PlatformAnthropic
		if shape == ShapeGemini {
			platform = PlatformGemini
		}
		if parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), platform); err == nil {
			parsed.ExplicitStickyKey = StickyKeyFromClientHeaders(c.Request.Header)
			parsed.SessionContext = &SessionContext{ClientIP: ip.GetClientIP(c), UserAgent: c.GetHeader("User-Agent"), APIKeyID: key.ID}
			session = r.candidateGateway.GenerateSessionHash(parsed)
		}
	} else {
		session = r.candidateOpenAI.GenerateSessionHash(c, body)
	}
	ctx, state, err := r.prepareCandidateRequest(ctx, key, shape, path, model, body, session, forcedPlatform, false, c.GetHeader("Content-Type"), candidateNewAPIPromptCacheSession(c, key, shape, model, body))
	if err != nil {
		return nil, err
	}
	c.Request = c.Request.WithContext(ctx)
	return state, nil
}
