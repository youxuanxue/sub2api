package service

import (
	"log/slog"
	"net/http"
	"strconv"
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
	tkSamplingParamRuleStripTemperature         tkSamplingParamRule = "strip_temperature"
	tkSamplingParamRuleStripTopP                tkSamplingParamRule = "strip_top_p"
	tkSamplingParamRuleStripTopK                tkSamplingParamRule = "strip_top_k"
)

var tkSamplingParamRules = gocache.New(tkSamplingParamRuleCacheTTL, tkSamplingParamRuleCacheCleanup)

func tkAnthropicCompatibilityCacheScope(account *Account) string {
	if account == nil || account.ID <= 0 {
		return ""
	}
	return strconv.FormatInt(account.ID, 10)
}

func tkSamplingParamRuleCacheKey(account *Account, model string, body []byte) string {
	scope := tkAnthropicCompatibilityCacheScope(account)
	if scope == "" {
		return ""
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	shape := tkSamplingParamRuleRequestShape(body)
	if shape == "" {
		return ""
	}
	return scope + "|" + model + "|" + shape
}

func tkSamplingParamRuleRequestShape(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var fields []string
	for _, field := range tkDeprecatedSamplingTopLevelFields {
		if gjson.GetBytes(body, field).Exists() {
			fields = append(fields, field)
		}
	}
	return strings.Join(fields, ",")
}

func tkGetCachedSamplingParamRule(account *Account, model string, body []byte) (tkSamplingParamRule, bool) {
	if tkSamplingParamRules == nil {
		return "", false
	}
	key := tkSamplingParamRuleCacheKey(account, model, body)
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

func tkPutCachedSamplingParamRule(account *Account, model string, requestBody []byte, rule tkSamplingParamRule) {
	if tkSamplingParamRules == nil || rule == "" {
		return
	}
	key := tkSamplingParamRuleCacheKey(account, model, requestBody)
	if key == "" {
		return
	}
	tkSamplingParamRules.Set(key, rule, gocache.DefaultExpiration)
}

func tkRecordAnthropicSamplingParamRuleFrom400(account *Account, model string, requestBody []byte, status int, body []byte) (tkSamplingParamRule, bool) {
	if account == nil || account.Platform != PlatformAnthropic {
		return "", false
	}
	rule, ok := tkSamplingParamRuleFromAnthropic400(model, status, body)
	if !ok {
		return "", false
	}
	tkPutCachedSamplingParamRule(account, model, requestBody, rule)
	slog.Info("tk_anthropic_sampling_param_rule_cache_populate",
		"account_id", account.ID,
		"model", strings.ToLower(strings.TrimSpace(model)),
		"rule", string(rule),
		"shape", tkSamplingParamRuleRequestShape(requestBody),
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
	if rule, ok := tkUnsupportedSamplingParamRuleFromMessage(msg); ok {
		return rule, true
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

func tkUnsupportedSamplingParamRuleFromMessage(lowerMessage string) (tkSamplingParamRule, bool) {
	if !tkIsUnsupportedSamplingParamMessage(lowerMessage) {
		return "", false
	}
	switch {
	case strings.Contains(lowerMessage, "temperature"):
		return tkSamplingParamRuleStripTemperature, true
	case strings.Contains(lowerMessage, "top_p"):
		return tkSamplingParamRuleStripTopP, true
	case strings.Contains(lowerMessage, "top_k"):
		return tkSamplingParamRuleStripTopK, true
	default:
		return "", false
	}
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

func tkApplyAnthropicRequestCompatibilityRules(account *Account, body []byte) []byte {
	return tkApplyCachedAnthropicThinkingRule(account, tkStripDeprecatedSamplingParamsForAccount(account, body))
}
