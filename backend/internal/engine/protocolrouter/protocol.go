package protocolrouter

import "fmt"

type Protocol string

const (
	ProtocolMessages        Protocol = "messages"
	ProtocolChatCompletions Protocol = "chat_completions"
	ProtocolResponses       Protocol = "responses"
)

func AllProtocols() []Protocol {
	return []Protocol{ProtocolMessages, ProtocolChatCompletions, ProtocolResponses}
}

func (p Protocol) Valid() bool {
	switch p {
	case ProtocolMessages, ProtocolChatCompletions, ProtocolResponses:
		return true
	default:
		return false
	}
}

func ParseProtocol(raw string) (Protocol, error) {
	protocol := Protocol(raw)
	if !protocol.Valid() {
		return "", fmt.Errorf("invalid protocol %q", raw)
	}
	return protocol, nil
}

type ResponsesPathKind string

const (
	ResponsesPathNone        ResponsesPathKind = ""
	ResponsesPathRoot        ResponsesPathKind = "root"
	ResponsesPathCompact     ResponsesPathKind = "compact"
	ResponsesPathInputTokens ResponsesPathKind = "input_tokens"
)

func (p ResponsesPathKind) Valid() bool {
	switch p {
	case ResponsesPathNone, ResponsesPathRoot, ResponsesPathCompact, ResponsesPathInputTokens:
		return true
	default:
		return false
	}
}

type ToolChoiceKind string

const (
	ToolChoiceNone     ToolChoiceKind = ""
	ToolChoiceAuto     ToolChoiceKind = "auto"
	ToolChoiceRequired ToolChoiceKind = "required"
	ToolChoiceNamed    ToolChoiceKind = "named"
)

type ContinuationKind string

const (
	ContinuationNone             ContinuationKind = ""
	ContinuationPreviousResponse ContinuationKind = "previous_response"
	ContinuationConversation     ContinuationKind = "conversation"
)

type ReasoningKind string

const (
	ReasoningNone    ReasoningKind = ""
	ReasoningEffort  ReasoningKind = "effort"
	ReasoningSummary ReasoningKind = "summary"
)

type PromptCacheKind string

const (
	PromptCacheNone      PromptCacheKind = ""
	PromptCacheKey       PromptCacheKind = "key"
	PromptCachePlacement PromptCacheKind = "placement"
)

type ContentKindSet uint32

const (
	ContentText ContentKindSet = 1 << iota
	ContentImage
	ContentAudio
	ContentFile
	ContentUnknown
)

type RequestProfile struct {
	Stream       bool
	Tools        bool
	ToolChoice   ToolChoiceKind
	Continuation ContinuationKind
	Reasoning    ReasoningKind
	PromptCache  PromptCacheKind
	ContentKinds ContentKindSet
}
