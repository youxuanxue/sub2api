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
	tkSamplingParamRuleCacheTTL     = 60 * time.Second
	tkSamplingParamRuleCacheCleanup = time.Minute
)

type tkSamplingParamRule string

const (
	tkSamplingParamRuleStripTopPWithTemperature tkSamplingParamRule = "strip_top_p_with_temperature"
	tkSamplingParamRuleStripAll                 tkSamplingParamRule = "strip_all_sampling"
)

var tkSamplingParamRules = gocache.New(tkSamplingParamRuleCacheTTL, tkSamplingParamRuleCacheCleanup)

func tkSamplingParamRuleCacheKey(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	return model
}

func tkGetCachedSamplingParamRule(model string) (tkSamplingParamRule, bool) {
	if tkSamplingParamRules == nil {
		return "", false
	}
	key := tkSamplingParamRuleCacheKey(model)
	if key == "" {
		return "", false
	}
	v, ok := tkSamplingParamRules.Get(key)
	if !ok {
		return "", false
	}
	rule, ok := v.(tkSamplingParamRule)
	return rule, ok
}

func tkPutCachedSamplingParamRule(model string, rule tkSamplingParamRule) {
	if tkSamplingParamRules == nil || rule == "" {
		return
	}
	key := tkSamplingParamRuleCacheKey(model)
	if key == "" {
		return
	}
	tkSamplingParamRules.Set(key, rule, gocache.DefaultExpiration)
}

func tkRecordAnthropicSamplingParamRuleFrom400(platform, model string, status int, body []byte) (tkSamplingParamRule, bool) {
	if platform != PlatformAnthropic {
		return "", false
	}
	rule, ok := tkSamplingParamRuleFromAnthropic400(model, status, body)
	if !ok {
		return "", false
	}
	tkPutCachedSamplingParamRule(model, rule)
	slog.Info("tk_anthropic_sampling_param_rule_cache_populate",
		"model", strings.ToLower(strings.TrimSpace(model)),
		"rule", string(rule),
		"ttl", tkSamplingParamRuleCacheTTL.String())
	return rule, true
}

func tkSamplingParamRuleFromAnthropic400(model string, status int, body []byte) (tkSamplingParamRule, bool) {
	if status != http.StatusBadRequest || strings.TrimSpace(model) == "" || !tkIsAnthropicInvalidRequestErrorBody(body) {
		return "", false
	}
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(string(body)))
	}
	if tkIsTemperatureTopPConflictMessage(msg) {
		return tkSamplingParamRuleStripTopPWithTemperature, true
	}
	if tkIsUnsupportedSamplingParamMessage(msg) {
		return tkSamplingParamRuleStripAll, true
	}
	return "", false
}

func tkIsAnthropicInvalidRequestErrorBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	return errType == "invalid_request_error"
}

func tkIsTemperatureTopPConflictMessage(lowerMessage string) bool {
	if !strings.Contains(lowerMessage, "temperature") || !strings.Contains(lowerMessage, "top_p") {
		return false
	}
	compact := strings.NewReplacer("`", "", "'", "", "\"", "", " ", "").Replace(lowerMessage)
	return strings.Contains(compact, "temperatureandtop_pcannotbothbespecified") ||
		(strings.Contains(compact, "eitheraltertemperatureortop_p") && strings.Contains(compact, "notboth"))
}

func tkIsUnsupportedSamplingParamMessage(lowerMessage string) bool {
	hasSamplingParam := strings.Contains(lowerMessage, "temperature") ||
		strings.Contains(lowerMessage, "top_p") ||
		strings.Contains(lowerMessage, "top_k")
	if !hasSamplingParam {
		return false
	}
	return strings.Contains(lowerMessage, "not supported for this model") ||
		strings.Contains(lowerMessage, "extra inputs are not permitted") ||
		strings.Contains(lowerMessage, "unsupported parameter") ||
		strings.Contains(lowerMessage, "unknown parameter") ||
		strings.Contains(lowerMessage, "unknown_parameter") ||
		strings.Contains(lowerMessage, "unrecognized request argument") ||
		strings.Contains(lowerMessage, "unrecognized parameter")
}

func tkApplyAnthropicRequestCompatibilityRules(body []byte) []byte {
	return tkApplyCachedAnthropicThinkingRule(tkStripDeprecatedSamplingParams(body))
}
