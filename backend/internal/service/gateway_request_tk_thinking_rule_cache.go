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

func tkAnthropicThinkingRuleCacheKey(account *Account, model string, body []byte) string {
	scope := tkAnthropicCompatibilityCacheScope(account)
	if scope == "" {
		return ""
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	shape := tkAnthropicThinkingRuleRequestShape(body)
	if shape == "" {
		return ""
	}
	return scope + "|" + model + "|" + shape
}

func tkAnthropicThinkingRuleRequestShape(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if gjson.GetBytes(body, "thinking.type").String() != "enabled" {
		return ""
	}
	return "thinking.type=enabled"
}

func tkGetCachedAnthropicThinkingRule(account *Account, model string, body []byte) (tkAnthropicThinkingRule, bool) {
	if tkAnthropicThinkingRules == nil {
		return "", false
	}
	key := tkAnthropicThinkingRuleCacheKey(account, model, body)
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

func tkPutCachedAnthropicThinkingRule(account *Account, model string, requestBody []byte, rule tkAnthropicThinkingRule) {
	if tkAnthropicThinkingRules == nil || rule == "" {
		return
	}
	key := tkAnthropicThinkingRuleCacheKey(account, model, requestBody)
	if key == "" {
		return
	}
	tkAnthropicThinkingRules.Set(key, rule, gocache.DefaultExpiration)
}

func tkRecordAnthropicThinkingRuleFrom400(account *Account, model string, requestBody []byte, status int, body []byte) (tkAnthropicThinkingRule, bool) {
	if account == nil || account.Platform != PlatformAnthropic {
		return "", false
	}
	rule, ok := tkAnthropicThinkingRuleFrom400(model, status, body)
	if !ok {
		return "", false
	}
	tkPutCachedAnthropicThinkingRule(account, model, requestBody, rule)
	slog.Info("tk_anthropic_thinking_rule_cache_populate",
		"account_id", account.ID,
		"model", strings.ToLower(strings.TrimSpace(model)),
		"rule", string(rule),
		"shape", tkAnthropicThinkingRuleRequestShape(requestBody),
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

func tkApplyCachedAnthropicThinkingRule(account *Account, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	rule, ok := tkGetCachedAnthropicThinkingRule(account, model, body)
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
