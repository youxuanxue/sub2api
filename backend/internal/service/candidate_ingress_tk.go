package service

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/gin-gonic/gin"
)

// PrepareCandidateIngress uses the existing client and session parsers before
// billing admission, when sticky-only eligibility already matters.
func (r *UniversalRoutingResolver) PrepareCandidateIngress(c *gin.Context, key *APIKey, shape UniversalShape, path, model string, body []byte, forcedPlatform string) (*CandidateRequest, error) {
	ctx := WithCandidateIdentity(c.Request.Context(), key.UserID, key.ID)
	var document map[string]any
	_ = json.Unmarshal(body, &document)
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
	ctx, state, err := r.PrepareCandidateRequest(ctx, key, shape, path, model, body, session, forcedPlatform)
	if err != nil {
		return nil, err
	}
	c.Request = c.Request.WithContext(ctx)
	return state, nil
}
