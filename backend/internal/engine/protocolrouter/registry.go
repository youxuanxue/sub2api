package protocolrouter

import (
	"errors"
	"fmt"
	"strings"
)

type RouteKind string

const (
	RouteIdentity   RouteKind = "identity"
	RouteConversion RouteKind = "conversion"
)

type RouteAdapterID string

const (
	AdapterMessagesIdentity    RouteAdapterID = "messages_identity"
	AdapterMessagesToResponses RouteAdapterID = "messages_to_responses"
	AdapterMessagesToChat      RouteAdapterID = "messages_to_chat_completions"
	AdapterChatIdentity        RouteAdapterID = "chat_completions_identity"
	AdapterChatToResponses     RouteAdapterID = "chat_completions_to_responses"
	AdapterChatToMessages      RouteAdapterID = "chat_completions_to_messages"
	AdapterResponsesIdentity   RouteAdapterID = "responses_identity"
	AdapterResponsesToChat     RouteAdapterID = "responses_to_chat_completions"
	AdapterResponsesToMessages RouteAdapterID = "responses_to_messages"
	AdapterMessagesToGemini    RouteAdapterID = "messages_to_gemini_generate_content"
	AdapterChatToGemini        RouteAdapterID = "chat_completions_to_gemini_generate_content"
	AdapterResponsesToGemini   RouteAdapterID = "responses_to_gemini_generate_content"
	AdapterGeminiIdentity      RouteAdapterID = "gemini_generate_content_identity"
)

type routeEntry struct {
	inbound        Protocol
	target         Protocol
	kind           RouteKind
	adapterID      RouteAdapterID
	transport      TransportID
	responsesPaths []ResponsesPathKind
	model          func(AccountSnapshot) bool
	preserves      func(CanonicalRequest) bool
	endpoint       func(AccountSnapshot, Protocol, ResponsesPathKind) (string, error)
}

// RouteSpec is an immutable read-only projection of one registry entry. It
// exists so mechanical contract tests can derive their cases from the routing
// SSOT instead of maintaining a second route matrix.
type RouteSpec struct {
	inbound   Protocol
	target    Protocol
	kind      RouteKind
	adapterID RouteAdapterID
	transport TransportID
}

func (s RouteSpec) InboundProtocol() Protocol { return s.inbound }
func (s RouteSpec) TargetProtocol() Protocol  { return s.target }
func (s RouteSpec) RouteKind() RouteKind      { return s.kind }
func (s RouteSpec) AdapterID() RouteAdapterID { return s.adapterID }
func (s RouteSpec) Transport() TransportID    { return s.transport }

func RouteSpecs() []RouteSpec {
	specs := make([]RouteSpec, len(routeRegistry))
	for i, route := range routeRegistry {
		specs[i] = RouteSpec{
			inbound:   route.inbound,
			target:    route.target,
			kind:      route.kind,
			adapterID: route.adapterID,
			transport: route.transport,
		}
	}
	return specs
}

var routeRegistry = []routeEntry{
	{inbound: ProtocolMessages, target: ProtocolMessages, kind: RouteIdentity, adapterID: AdapterMessagesIdentity, transport: TransportHTTP, model: permitsMessagesModel, preserves: preservesIdentity, endpoint: resolveEndpoint},
	{inbound: ProtocolMessages, target: ProtocolResponses, kind: RouteConversion, adapterID: AdapterMessagesToResponses, transport: TransportHTTP, responsesPaths: []ResponsesPathKind{ResponsesPathRoot}, model: permitsResponsesModel, preserves: preservesMessagesToResponses, endpoint: resolveEndpoint},
	{inbound: ProtocolMessages, target: ProtocolChatCompletions, kind: RouteConversion, adapterID: AdapterMessagesToChat, transport: TransportHTTP, model: permitsChatCompletionsModel, preserves: preservesMessagesToChat, endpoint: resolveEndpoint},
	{inbound: ProtocolMessages, target: ProtocolGeminiGenerateContent, kind: RouteConversion, adapterID: AdapterMessagesToGemini, transport: TransportHTTP, model: permitsGeminiModel, preserves: preservesToGemini, endpoint: resolveEndpoint},
	{inbound: ProtocolChatCompletions, target: ProtocolChatCompletions, kind: RouteIdentity, adapterID: AdapterChatIdentity, transport: TransportHTTP, model: permitsChatCompletionsModel, preserves: preservesIdentity, endpoint: resolveEndpoint},
	{inbound: ProtocolChatCompletions, target: ProtocolResponses, kind: RouteConversion, adapterID: AdapterChatToResponses, transport: TransportHTTP, responsesPaths: []ResponsesPathKind{ResponsesPathRoot}, model: permitsResponsesModel, preserves: preservesChatToResponses, endpoint: resolveEndpoint},
	{inbound: ProtocolChatCompletions, target: ProtocolMessages, kind: RouteConversion, adapterID: AdapterChatToMessages, transport: TransportHTTP, model: permitsMessagesModel, preserves: preservesChatToMessages, endpoint: resolveEndpoint},
	{inbound: ProtocolChatCompletions, target: ProtocolGeminiGenerateContent, kind: RouteConversion, adapterID: AdapterChatToGemini, transport: TransportHTTP, model: permitsGeminiModel, preserves: preservesToGemini, endpoint: resolveEndpoint},
	{inbound: ProtocolResponses, target: ProtocolResponses, kind: RouteIdentity, adapterID: AdapterResponsesIdentity, transport: TransportHTTP, responsesPaths: []ResponsesPathKind{ResponsesPathRoot, ResponsesPathCompact, ResponsesPathInputTokens}, model: permitsResponsesModel, preserves: preservesIdentity, endpoint: resolveEndpoint},
	{inbound: ProtocolResponses, target: ProtocolChatCompletions, kind: RouteConversion, adapterID: AdapterResponsesToChat, transport: TransportHTTP, model: permitsChatCompletionsModel, preserves: preservesResponsesConversion, endpoint: resolveEndpoint},
	{inbound: ProtocolResponses, target: ProtocolMessages, kind: RouteConversion, adapterID: AdapterResponsesToMessages, transport: TransportHTTP, model: permitsMessagesModel, preserves: preservesResponsesConversion, endpoint: resolveEndpoint},
	{inbound: ProtocolResponses, target: ProtocolGeminiGenerateContent, kind: RouteConversion, adapterID: AdapterResponsesToGemini, transport: TransportHTTP, model: permitsGeminiModel, preserves: preservesToGemini, endpoint: resolveEndpoint},
	{inbound: ProtocolGeminiGenerateContent, target: ProtocolGeminiGenerateContent, kind: RouteIdentity, adapterID: AdapterGeminiIdentity, transport: TransportHTTP, model: permitsGeminiModel, preserves: preservesGeminiIdentity, endpoint: resolveEndpoint},
}

func validateRouteRegistry(entries []routeEntry) error {
	if len(entries) == 0 {
		return errors.New("route registry is empty")
	}
	seen := make(map[string]struct{}, len(entries))
	for index, route := range entries {
		if !route.inbound.Valid() || !route.target.Valid() {
			return fmt.Errorf("route %d has invalid protocol", index)
		}
		if route.kind != RouteIdentity && route.kind != RouteConversion {
			return fmt.Errorf("route %d has invalid kind %q", index, route.kind)
		}
		if route.adapterID == "" || strings.TrimSpace(string(route.transport)) == "" {
			return fmt.Errorf("route %d requires adapter and transport", index)
		}
		if route.model == nil || route.preserves == nil || route.endpoint == nil {
			return fmt.Errorf("route %d has incomplete execution policy", index)
		}
		if route.target == ProtocolResponses {
			if len(route.responsesPaths) == 0 {
				return fmt.Errorf("route %d requires explicit responses paths", index)
			}
			pathSeen := make(map[ResponsesPathKind]struct{}, len(route.responsesPaths))
			for _, responsePath := range route.responsesPaths {
				if responsePath == ResponsesPathNone || !responsePath.Valid() {
					return fmt.Errorf("route %d has invalid responses path %q", index, responsePath)
				}
				if _, duplicate := pathSeen[responsePath]; duplicate {
					return fmt.Errorf("route %d duplicates responses path %q", index, responsePath)
				}
				pathSeen[responsePath] = struct{}{}
			}
		} else if len(route.responsesPaths) != 0 {
			return fmt.Errorf("route %d declares responses paths for target %q", index, route.target)
		}
		key := string(route.inbound) + "\x00" + string(route.target)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate route %s -> %s", route.inbound, route.target)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func routePermitsResponsesPath(route routeEntry, responsePath ResponsesPathKind) bool {
	if route.target != ProtocolResponses {
		return responsePath == ResponsesPathNone
	}
	for _, allowed := range route.responsesPaths {
		if allowed == responsePath {
			return true
		}
	}
	return false
}

func permitsMessagesModel(account AccountSnapshot) bool {
	return account.permitsModel(ProtocolMessages)
}

func permitsChatCompletionsModel(account AccountSnapshot) bool {
	return account.permitsModel(ProtocolChatCompletions)
}

func permitsResponsesModel(account AccountSnapshot) bool {
	return account.permitsModel(ProtocolResponses)
}

func permitsGeminiModel(account AccountSnapshot) bool {
	return account.permitsModel(ProtocolGeminiGenerateContent)
}

func preservesIdentity(CanonicalRequest) bool { return true }

func preservesMessagesToResponses(req CanonicalRequest) bool {
	// ForwardAsAnthropic already preserves Claude Code tools, images, and
	// tool_use / tool_result blocks. Text-only-without-tools was fail-closing
	// those requests onto /v1/messages identity, which TokenKey edge OpenAI
	// pools cannot serve.
	return preservesMessagesToResponsesContent(req) &&
		req.profile.Continuation == ContinuationNone
}

func preservesMessagesToResponsesContent(req CanonicalRequest) bool {
	if req.profile.ContentKinds == 0 {
		return false
	}
	allowed := ContentText | ContentImage | ContentUnknown
	return req.profile.ContentKinds&^allowed == 0
}

func preservesMessagesToChat(req CanonicalRequest) bool {
	// forwardAnthropicViaRawChatCompletions already converts Claude Code
	// function tools and images (AnthropicToChatCompletionsRequest). The old
	// text-only-without-tools gate fail-closed those requests onto messages
	// identity, which dual-stack OpenAI relays cannot serve for non-Claude models.
	return preservesMessagesToResponsesContent(req) &&
		req.profile.Continuation == ContinuationNone
}

func preservesChatToResponses(req CanonicalRequest) bool {
	return preservesTextOnlyWithoutTools(req) &&
		req.profile.Continuation == ContinuationNone
}

func preservesChatToMessages(req CanonicalRequest) bool {
	return preservesTextOnlyWithoutTools(req) &&
		req.profile.Continuation == ContinuationNone &&
		req.profile.Reasoning == ReasoningNone &&
		req.profile.PromptCache == PromptCacheNone
}

func preservesResponsesConversion(req CanonicalRequest) bool {
	return preservesTextOnlyWithoutTools(req) &&
		req.responsesPath == ResponsesPathRoot &&
		req.profile.Continuation == ContinuationNone &&
		req.profile.Reasoning == ReasoningNone &&
		req.profile.PromptCache == PromptCacheNone
}

func preservesToGemini(req CanonicalRequest) bool {
	return preservesTextOnlyWithoutTools(req) &&
		(req.inboundProtocol != ProtocolResponses || req.responsesPath == ResponsesPathRoot) &&
		req.profile.Continuation == ContinuationNone &&
		req.profile.Reasoning == ReasoningNone &&
		req.profile.PromptCache == PromptCacheNone
}

func preservesGeminiIdentity(req CanonicalRequest) bool {
	return req.profile.ContentKinds != 0 && req.profile.ContentKinds&^ContentText == 0
}

func preservesTextOnlyWithoutTools(req CanonicalRequest) bool {
	return !req.profile.Tools &&
		req.profile.ToolChoice == ToolChoiceNone &&
		req.profile.ContentKinds != 0 &&
		req.profile.ContentKinds&^ContentText == 0
}
