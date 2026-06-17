package trajectory

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// WireShape identifies the client-facing protocol shape of a captured QARecord's
// evidence blob. The traj v2 projector dispatches reconstruction by shape: each
// shape parses its own request.messages / response / SSE layout but every builder
// emits the SAME TrajSessionV2/TrajTurnV2 vocabulary (text / thinking / tool_use
// blocks; user / assistant / tool turns), so the export schema stays uniform
// across platforms (anthropic / openai / gemini / antigravity / kiro / newapi).
//
// Shape is a pure function of (platform, normalized inbound_endpoint) — both
// already stored on every QARecord by the capture layer. It is NOT the upstream
// shape: antigravity relays to Gemini cloudcode-pa internally, but the captured
// client-facing blob on /antigravity/v1 is Anthropic-shaped and on
// /antigravity/v1beta is Gemini-shaped, so antigravity reuses the anthropic /
// gemini builders by inbound endpoint rather than needing its own.
type WireShape string

const (
	WireAnthropicMessages WireShape = "anthropic_messages" // /v1/messages (anthropic, kiro, antigravity v1)
	WireOpenAIChat        WireShape = "openai_chat"        // /v1/chat/completions (openai, newapi, grok)
	WireOpenAIResponses   WireShape = "openai_responses"   // /v1/responses (Codex)
	WireGemini            WireShape = "gemini"             // /v1beta/models ...generateContent (gemini, vertex, antigravity v1beta, newapi ch41)
	WireUnknown           WireShape = ""                   // non-conversation (embeddings/images/video) or unprojectable
)

// Canonical normalized inbound-endpoint markers. These mirror the values
// normalizeInboundEndpoint produces in the qa package (qaEndpointMessages, …).
// They are public, stable Anthropic/OpenAI/Gemini API paths — external contracts,
// not a TokenKey platform list — so matching on the literals here (the qa package
// cannot be imported without a cycle) is robust, and a qa-package endpoint-shape
// test (TestWireShapeMatchesCaptureEndpoints) guards against drift.
const (
	endpointMessages        = "/v1/messages"
	endpointChatCompletions = "/v1/chat/completions"
	endpointResponses       = "/v1/responses"
	endpointGeminiModels    = "/v1beta/models"
)

// WireShapeForRecord returns the wire shape of a captured record. The normalized
// inbound endpoint is the primary discriminator (it already disambiguates
// newapi's chat-vs-gemini records and antigravity's v1-vs-v1beta surfaces); the
// platform is only a fallback for records whose endpoint carries no conversation
// marker (so a single-shape platform still projects), and otherwise Unknown so
// the projector skips rather than emit garbage turns.
func WireShapeForRecord(rec *ent.QARecord) WireShape {
	if rec == nil {
		return WireUnknown
	}
	return wireShapeFor(rec.Platform, rec.InboundEndpoint)
}

func wireShapeFor(platform, inboundEndpoint string) WireShape {
	ep := strings.TrimSpace(inboundEndpoint)
	switch {
	case strings.Contains(ep, endpointResponses):
		return WireOpenAIResponses
	case strings.Contains(ep, endpointChatCompletions):
		return WireOpenAIChat
	case strings.Contains(ep, endpointMessages):
		return WireAnthropicMessages
	case strings.Contains(ep, endpointGeminiModels):
		return WireGemini
	}
	// Endpoint carried no conversation marker (embeddings/images/video, or an
	// empty/odd value). Only platforms with exactly ONE canonical conversation
	// shape get a fallback — openai/newapi/grok speak both chat and responses, so
	// without an endpoint marker they are Unknown (skip) rather than guessed.
	switch platform {
	case domain.PlatformAnthropic, domain.PlatformKiro, domain.PlatformAntigravity:
		return WireAnthropicMessages
	case domain.PlatformGemini:
		return WireGemini
	default:
		return WireUnknown
	}
}

// ProjectableShape reports whether the projector has a builder for this shape.
func ProjectableShape(s WireShape) bool { return s != WireUnknown }
