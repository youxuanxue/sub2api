package service

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// NewAPI / Volc prompt-cache sticky affinity.
//
// Account sticky already exists for NewAPI groups, but GenerateSessionHash's
// default content seed includes the first user turn. Independent prompts that
// share a long system/tool prefix (Agent / acceptance "repeat group") therefore
// hash differently and load-balance across pool members, destroying upstream
// prefix cache. Grok already scopes cache identity by stable prefix; NewAPI
// groups reuse that idea for *account* sticky so the same session keeps one
// upstream account (prod or edge mirror — mirrors apply the same sticky rule).

func newAPIGroupPromptCacheStickyEnabled(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if c.Request != nil && IsUniversalKeyRouting(c.Request.Context()) {
		return false
	}
	v, ok := c.Get("api_key")
	if !ok {
		return false
	}
	apiKey, ok := v.(*APIKey)
	if !ok || apiKey == nil || apiKey.Group == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(apiKey.Group.Platform), PlatformNewAPI)
}

// deriveNewAPIPromptCacheSessionSeed builds a sticky seed that stays stable
// across independent user turns that share system/tools/instructions.
// Scoped by api_key_id (+ model) so unrelated tenants never share a binding.
func deriveNewAPIPromptCacheSessionSeed(c *gin.Context, body []byte) string {
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID <= 0 {
		return deriveOpenAIContentSessionSeed(body)
	}

	model := ""
	if len(body) > 0 {
		model = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	}

	seed := deriveOpenAIStablePrefixSessionSeed(body)
	if seed == "" {
		// No reusable prefix: keep first-user anchoring so model-only bodies
		// do not collapse an entire API key onto one account.
		seed = deriveOpenAIAnchoredContentSessionSeed(body)
	}
	if seed == "" {
		return ""
	}
	return fmt.Sprintf("newapi-prompt-cache:v1:%d:%s:%s", apiKeyID, model, seed)
}
