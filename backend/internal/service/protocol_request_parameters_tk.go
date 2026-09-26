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
	if isNewAPINVIDIABuildAccount(account) && resolvedModel == "deepseek-ai/deepseek-v4-pro-0813" &&
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
	wire, ok := cursorExecutionWire(request)
	if !ok {
		return false
	}
	return cursorWireContentSupported(wire, WebSearchHistoryStripsAllBlocks(resolvedModel))
}

// cursorExecutionWire projects the request onto the Messages wire that reaches
// Cursor's parser. It reads only the request body and its inbound protocol, both
// immutable within one request, so a caller evaluating many routes over one body
// derives the wire once. Returns false when the projection itself fails, which
// is the existing unsupported answer for every model.
func cursorExecutionWire(request protocolrouter.CanonicalRequest) ([]byte, bool) {
	body := request.Body()
	var converted *apicompat.AnthropicRequest
	var err error
	switch request.InboundProtocol() {
	case protocolrouter.ProtocolMessages:
		// Native Messages already has the execution wire shape.
	case protocolrouter.ProtocolChatCompletions:
		var input apicompat.ChatCompletionsRequest
		if json.Unmarshal(body, &input) != nil {
			return nil, false
		}
		converted, err = apicompat.ChatCompletionsToAnthropicRequest(&input)
	case protocolrouter.ProtocolResponses:
		body, _, err = adaptResponsesClientToolsForAnthropic(body)
		if err != nil {
			return nil, false
		}
		var input apicompat.ResponsesRequest
		if json.Unmarshal(body, &input) != nil {
			return nil, false
		}
		converted, err = apicompat.ResponsesToAnthropicRequest(&input)
	default:
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	if converted != nil {
		body, err = json.Marshal(converted)
		if err != nil {
			return nil, false
		}
	}
	// StripEmptyTextBlocks does not read the model, so it belongs to the
	// body-derived half; only the web-search strip depends on it.
	return StripEmptyTextBlocks(body), true
}

// cursorWireContentSupported is the model-dependent half. stripAll must come
// from WebSearchHistoryStripsAllBlocks, the sole channel through which a model
// reaches this decision.
func cursorWireContentSupported(wire []byte, stripAll bool) bool {
	return cursor.ValidateMessagesContent(filterWebSearchHistoryBlocks(wire, stripAll)) == nil
}

// Cache only immutable content validation, shared across candidate accounts and
// conversion permissions. Account and endpoint snapshots still refresh normally.
type cursorRequestContentCache struct {
	mu sync.Mutex
	// wires memoizes the body-derived projection per request. Distinct resolved
	// models share one entry because the projection never reads the model.
	wires    map[protocolrouter.RequestDigest]cursorExecutionWireResult
	outcomes map[cursorRequestContentKey]bool
}

type cursorExecutionWireResult struct {
	wire []byte
	ok   bool
}

// cursorRequestContentKey keys the outcome on stripAll rather than on the model
// string: models that agree on it cannot disagree on the answer, so keying by
// model would re-run the conversion once per account mapping.
type cursorRequestContentKey struct {
	digest   protocolrouter.RequestDigest
	stripAll bool
}

func (c *cursorRequestContentCache) supported(request protocolrouter.CanonicalRequest, model string) bool {
	if c == nil {
		// No cache to share a projection through, so this is exactly the
		// uncached decision; keep one owner for it rather than inlining it.
		return cursorProtocolContentSupported(request, model)
	}
	stripAll := WebSearchHistoryStripsAllBlocks(model)
	c.mu.Lock()
	defer c.mu.Unlock()
	digest := request.Digest()
	key := cursorRequestContentKey{digest: digest, stripAll: stripAll}
	if supported, ok := c.outcomes[key]; ok {
		return supported
	}
	projection, cached := c.wires[digest]
	if !cached {
		projection.wire, projection.ok = cursorExecutionWire(request)
		if c.wires == nil {
			c.wires = make(map[protocolrouter.RequestDigest]cursorExecutionWireResult)
		}
		c.wires[digest] = projection
	}
	supported := projection.ok && cursorWireContentSupported(projection.wire, stripAll)
	if c.outcomes == nil {
		c.outcomes = make(map[cursorRequestContentKey]bool)
	}
	c.outcomes[key] = supported
	return supported
}
