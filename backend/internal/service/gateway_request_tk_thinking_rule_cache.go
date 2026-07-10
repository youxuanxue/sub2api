package service

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/tidwall/gjson"
)

const (
	tkAnthropicThinkingRuleCacheTTL     = 60 * time.Second
	tkAnthropicThinkingRuleCacheCleanup = time.Minute
)

type tkAnthropicThinkingRule string

const (
	tkAnthropicThinkingRuleAdaptiveOnly tkAnthropicThinkingRule = "thinking_type_adaptive"
)

var tkAnthropicThinkingRules = gocache.New(tkAnthropicThinkingRuleCacheTTL, tkAnthropicThinkingRuleCacheCleanup)

func tkAnthropicThinkingRuleCacheKey(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	return model
}

func tkGetCachedAnthropicThinkingRule(model string) (tkAnthropicThinkingRule, bool) {
	if tkAnthropicThinkingRules == nil {
		return "", false
	}
	key := tkAnthropicThinkingRuleCacheKey(model)
	if key == "" {
		return "", false
	}
	v, ok := tkAnthropicThinkingRules.Get(key)
	if !ok {
		return "", false
	}
	rule, ok := v.(tkAnthropicThinkingRule)
	return rule, ok
}

func tkPutCachedAnthropicThinkingRule(model string, rule tkAnthropicThinkingRule) {
	if tkAnthropicThinkingRules == nil || rule == "" {
		return
	}
	key := tkAnthropicThinkingRuleCacheKey(model)
	if key == "" {
		return
	}
	tkAnthropicThinkingRules.Set(key, rule, gocache.DefaultExpiration)
}

func tkRecordAnthropicThinkingRuleFrom400(platform, model string, status int, body []byte) (tkAnthropicThinkingRule, bool) {
	if platform != PlatformAnthropic {
		return "", false
	}
	rule, ok := tkAnthropicThinkingRuleFrom400(model, status, body)
	if !ok {
		return "", false
	}
	tkPutCachedAnthropicThinkingRule(model, rule)
	slog.Info("tk_anthropic_thinking_rule_cache_populate",
		"model", strings.ToLower(strings.TrimSpace(model)),
		"rule", string(rule),
		"ttl", tkAnthropicThinkingRuleCacheTTL.String())
	return rule, true
}

func tkAnthropicThinkingRuleFrom400(model string, status int, body []byte) (tkAnthropicThinkingRule, bool) {
	if status != http.StatusBadRequest || strings.TrimSpace(model) == "" || !tkIsAnthropicInvalidRequestErrorBody(body) {
		return "", false
	}
	errMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if errMsg == "" {
		errMsg = strings.TrimSpace(string(body))
	}
	if isThinkingTypeAdaptiveRequiredError(errMsg) {
		return tkAnthropicThinkingRuleAdaptiveOnly, true
	}
	return "", false
}

func tkApplyCachedAnthropicThinkingRule(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	rule, ok := tkGetCachedAnthropicThinkingRule(model)
	if !ok {
		return body
	}
	switch rule {
	case tkAnthropicThinkingRuleAdaptiveOnly:
		if rectified, applied := RectifyThinkingTypeAdaptive(body); applied {
			return rectified
		}
	}
	return body
}
