package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeChatProviderRequest runs at the selected Chat destination, including
// converted Chat requests. Never mutate the ingress body used by another attempt.
// JSON modes are deliberately untouched: json_object is not a schema guarantee.
func normalizeChatProviderRequest(account *Account, targetURL string, body []byte) ([]byte, error) {
	body, err := normalizeStrictChatDeveloperRoles(account, targetURL, body)
	if err != nil {
		return nil, err
	}
	if account == nil || account.Type != AccountTypeAPIKey {
		return body, nil
	}
	base := strings.TrimSuffix(strings.TrimRight(targetURL, "/"), "/chat/completions")
	if !newapiintegration.IsAliTokenPlanBaseURL(newapiconstant.ChannelTypeAli, base) &&
		!newapiintegration.IsVolcEngineAgentPlanBaseURL(newapiconstant.ChannelTypeVolcEngine, base) {
		return body, nil
	}
	body = coalescePlanChatTokenLimit(body)
	return normalizeChatToolConstraints(body)
}

// coalescePlanChatTokenLimit collapses OpenAI-style dual output limits that
// Volc/Ali Plan reject with 400 "max_tokens and max_completion_tokens cannot
// be set at the same time". Prefer a positive max_completion_tokens budget
// (Chat converter priority), map it onto max_tokens, and drop
// max_completion_tokens. Invalid or non-positive values are left untouched.
func coalescePlanChatTokenLimit(body []byte) []byte {
	value := gjson.GetBytes(body, "max_completion_tokens")
	if !value.Exists() || value.Type == gjson.Null {
		return body
	}
	limit, err := strconv.ParseInt(value.Raw, 10, 64)
	if err != nil || limit <= 0 {
		return body
	}
	next, err := sjson.SetRawBytes(body, "max_tokens", []byte(value.Raw))
	if err != nil {
		return body
	}
	next, err = sjson.DeleteBytes(next, "max_completion_tokens")
	if err != nil {
		return body
	}
	return next
}

func normalizeBridgeChatProviderRequest(account *Account, in bridge.ChannelContextInput, body []byte) ([]byte, error) {
	if !newapiintegration.IsAliTokenPlanBaseURL(newapiconstant.ChannelTypeAli, in.BaseURL) &&
		!newapiintegration.IsVolcEngineAgentPlanBaseURL(newapiconstant.ChannelTypeVolcEngine, in.BaseURL) {
		return body, nil
	}
	endpoint, err := bridge.ResolveTextEndpoint(in, newapitypes.RelayFormatOpenAI, gjson.GetBytes(body, "model").String())
	if err != nil {
		return nil, err
	}
	return normalizeChatProviderRequest(account, endpoint, body)
}

type chatToolConstraint struct {
	none, required bool
	name           string
}

func parseChatToolConstraint(raw json.RawMessage, legacy bool) (chatToolConstraint, error) {
	var constraint chatToolConstraint
	if len(raw) == 0 || string(raw) == "null" {
		return constraint, nil
	}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		switch mode {
		case "none":
			constraint.none = true
		case "required":
			constraint.required = true
		case "auto":
		default:
			return constraint, errors.New("invalid tool selection mode")
		}
		return constraint, nil
	}
	var choice struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &choice) != nil {
		return constraint, errors.New("invalid tool selection object")
	}
	constraint.name = choice.Name
	if !legacy {
		constraint.name = choice.Function.Name
		if choice.Type != "function" {
			return constraint, errors.New("tool selection must name a function")
		}
	}
	if strings.TrimSpace(constraint.name) == "" {
		return constraint, errors.New("tool selection requires a function name")
	}
	return constraint, nil
}

func normalizeChatToolConstraints(body []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return nil, errors.New("tool constraints: invalid request object")
	}
	modern, err := parseChatToolConstraint(root["tool_choice"], false)
	if err != nil {
		return nil, err
	}
	legacy, err := parseChatToolConstraint(root["function_call"], true)
	if err != nil {
		return nil, err
	}
	if (modern.none && (legacy.name != "" || legacy.required)) ||
		(legacy.none && (modern.name != "" || modern.required)) ||
		(modern.name != "" && legacy.name != "" && modern.name != legacy.name) {
		return nil, errors.New("conflicting tool_choice and function_call constraints")
	}
	if modern.none || legacy.none {
		delete(root, "tools")
		delete(root, "functions")
		// Both tool APIs must agree; an alternate auto selector must not expose
		// a second tool entry point. Historical messages remain intact.
		for _, key := range []string{"tool_choice", "function_call"} {
			if _, exists := root[key]; exists {
				root[key] = json.RawMessage(`"none"`)
			}
		}
		return json.Marshal(root)
	}
	name := modern.name
	if name == "" {
		name = legacy.name
	}
	if name == "" {
		return body, nil
	}
	for _, key := range []string{"tools", "functions"} {
		var definitions []json.RawMessage
		if raw, exists := root[key]; exists && json.Unmarshal(raw, &definitions) != nil {
			return nil, errors.New("tool definitions must be an array")
		}
		selected := make([]json.RawMessage, 0, 1)
		for _, definition := range definitions {
			tool := gjson.ParseBytes(definition)
			candidate := tool.Get("name").String()
			if key == "tools" {
				if tool.Get("type").String() != "function" {
					continue
				}
				candidate = tool.Get("function.name").String()
			}
			if candidate == name {
				selected = append(selected, definition)
			}
		}
		required := (key == "tools" && modern.name != "") || (key == "functions" && legacy.name != "")
		if len(selected) > 1 || (required && len(selected) != 1) {
			return nil, errors.New("selected function must have exactly one definition")
		}
		if len(selected) == 0 {
			delete(root, key)
		} else {
			root[key], err = json.Marshal(selected)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(root)
}
