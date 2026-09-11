package service

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/tidwall/gjson"
)

// These exclusions preserve explicit client intent on empirically rejected
// provider/model paths. They do not remove parameters or alter model mappings.
func protocolRequestParametersSupported(account *Account, resolvedModel string, request *protocolrouter.CanonicalRequest, content *cursorRequestContentCache) bool {
	if request == nil {
		return true
	}
	if account.IsCursor() {
		choice := request.Profile().ToolChoice
		return choice != protocolrouter.ToolChoiceRequired && choice != protocolrouter.ToolChoiceNamed &&
			content.supported(*request, resolvedModel)
	}
	if isNewAPINVIDIABuildAccount(account) && resolvedModel == nvidiaBuildModelTargets["deepseek-v4-pro"] &&
		request.InboundProtocol() == protocolrouter.ProtocolChatCompletions {
		return !gjson.GetBytes(request.Body(), "enable_thinking").Exists()
	}
	if account.Platform != PlatformGrok {
		return true
	}
	model := strings.ToLower(xai.StripGrokProviderPrefix(strings.TrimSpace(resolvedModel)))
	if model != "grok-4.6" && model != "grok-4.6-latest" {
		return true
	}
	var fields []string
	switch request.InboundProtocol() {
	case protocolrouter.ProtocolChatCompletions:
		fields = []string{"reasoning_effort", "reasoningEffort"}
	case protocolrouter.ProtocolResponses:
		fields = []string{"reasoning.effort", "reasoning_effort", "reasoningEffort"}
	default:
		return true
	}
	body := request.Body()
	for _, field := range fields {
		value := gjson.GetBytes(body, field)
		if value.Type != gjson.String {
			continue
		}
		if normalized, keep := normalizeGrokReasoningEffortValue(value.String(), resolvedModel); keep && normalized == "none" {
			return false
		}
	}
	return true
}

// Cursor's native parser owns supported content, including nested tool results.
// Project through the execution converters so image/thinking history is judged
// on the Messages wire that will actually reach that parser.
func cursorProtocolContentSupported(request protocolrouter.CanonicalRequest, resolvedModel string) bool {
	body := request.Body()
	var converted *apicompat.AnthropicRequest
	var err error
	switch request.InboundProtocol() {
	case protocolrouter.ProtocolMessages:
		// Native Messages already has the execution wire shape.
	case protocolrouter.ProtocolChatCompletions:
		var input apicompat.ChatCompletionsRequest
		if json.Unmarshal(body, &input) != nil {
			return false
		}
		converted, err = apicompat.ChatCompletionsToAnthropicRequest(&input)
	case protocolrouter.ProtocolResponses:
		body, _, err = adaptResponsesClientToolsForAnthropic(body)
		if err != nil {
			return false
		}
		var input apicompat.ResponsesRequest
		if json.Unmarshal(body, &input) != nil {
			return false
		}
		converted, err = apicompat.ResponsesToAnthropicRequest(&input)
	default:
		return false
	}
	if err != nil {
		return false
	}
	if converted != nil {
		body, err = json.Marshal(converted)
		if err != nil {
			return false
		}
	}
	body = normalizeCursorMessagesContent(body, resolvedModel)
	return cursor.ValidateMessagesContent(body) == nil
}

// Cache only immutable content validation, shared across candidate accounts and
// conversion permissions. Account and endpoint snapshots still refresh normally.
type cursorRequestContentCache struct {
	mu       sync.Mutex
	outcomes map[cursorRequestContentKey]bool
}
type cursorRequestContentKey struct {
	digest protocolrouter.RequestDigest
	model  string
}

func (c *cursorRequestContentCache) supported(request protocolrouter.CanonicalRequest, model string) bool {
	if c == nil {
		return cursorProtocolContentSupported(request, model)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cursorRequestContentKey{digest: request.Digest(), model: model}
	if supported, ok := c.outcomes[key]; ok {
		return supported
	}
	supported := cursorProtocolContentSupported(request, model)
	if c.outcomes == nil {
		c.outcomes = make(map[cursorRequestContentKey]bool)
	}
	c.outcomes[key] = supported
	return supported
}
