package service

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

const openAICompatMinCompactionInputTokensThreshold = 1

type openAICompatMessagesCompactionPolicy struct {
	enabled         bool
	inputTokenLimit int
}

func resolveOpenAICompatMessagesCompactionPolicy(account *Account, group *Group) openAICompatMessagesCompactionPolicy {
	if accountEnabled, accountEnabledSet := accountExtraBool(account, "messages_compaction_enabled"); accountEnabledSet {
		if !accountEnabled {
			return openAICompatMessagesCompactionPolicy{}
		}
		if accountLimit, accountLimitSet := accountExtraInt(account, "messages_compaction_input_tokens_threshold"); accountLimitSet {
			if accountLimit < openAICompatMinCompactionInputTokensThreshold {
				return openAICompatMessagesCompactionPolicy{}
			}
			return openAICompatMessagesCompactionPolicy{enabled: true, inputTokenLimit: accountLimit}
		}
		return openAICompatMessagesCompactionPolicy{}
	}

	if group != nil && group.MessagesCompactionEnabled != nil {
		if !*group.MessagesCompactionEnabled {
			return openAICompatMessagesCompactionPolicy{}
		}
		if group.MessagesCompactionInputTokensThreshold != nil && *group.MessagesCompactionInputTokensThreshold >= openAICompatMinCompactionInputTokensThreshold {
			return openAICompatMessagesCompactionPolicy{enabled: true, inputTokenLimit: *group.MessagesCompactionInputTokensThreshold}
		}
		return openAICompatMessagesCompactionPolicy{}
	}

	return openAICompatMessagesCompactionPolicy{}
}

func shouldApplyOpenAICompatMessagesCompaction(policy openAICompatMessagesCompactionPolicy, req *apicompat.AnthropicRequest) bool {
	if !policy.enabled || policy.inputTokenLimit < openAICompatMinCompactionInputTokensThreshold || req == nil {
		return false
	}
	return estimateAnthropicRequestInputTokens(req) > policy.inputTokenLimit
}

func applyOpenAICompatMessagesCompaction(req *apicompat.AnthropicRequest) bool {
	if req == nil {
		return false
	}
	return applyAnthropicCompatFullReplayGuard(req)
}

func accountExtraBool(account *Account, key string) (bool, bool) {
	if account == nil || account.Extra == nil {
		return false, false
	}
	raw, ok := account.Extra[key]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(v))
		if trimmed == "true" {
			return true, true
		}
		if trimmed == "false" {
			return false, true
		}
	}
	return false, false
}

func accountExtraInt(account *Account, key string) (int, bool) {
	if account == nil || account.Extra == nil {
		return 0, false
	}
	raw, ok := account.Extra[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i64, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(i64), true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		parsed := gjson.Parse(trimmed)
		if !parsed.Exists() || parsed.Type != gjson.Number {
			return 0, false
		}
		return int(parsed.Int()), true
	}
	return 0, false
}

func estimateAnthropicRequestInputTokens(req *apicompat.AnthropicRequest) int {
	if req == nil {
		return 0
	}
	total := 0
	if len(req.System) > 0 {
		total += estimateTokensForText(string(req.System))
	}
	for _, msg := range req.Messages {
		total += estimateTokensForText(string(msg.Content))
	}
	if total < 0 {
		return 0
	}
	return total
}
