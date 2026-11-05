package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
)

var ErrUnsupportedModel = errors.New("unsupported model")

// Unsupported / unknown model wire contract — SSOT owner.
// Contract: docs/approved/unsupported-model-404-ssot.md.
//
// Every gateway-minted "this model is not served here" response uses HTTP 404,
// matching the official APIs (OpenAI 404 model_not_found, Anthropic 404
// not_found_error, Gemini 404 NOT_FOUND). Emit sites pass these values (or call
// TkWriteUnsupportedModel); they never hardcode a status. "Model exists but this
// request shape is unservable" stays TkUnsupportedRequest* (400) and empty pool
// / capacity stays 429.
const (
	TkUnsupportedModelStatus = http.StatusNotFound
	// TkUnsupportedModelErrType is the OpenAI error.type; Anthropic/Gemini
	// writers translate it through TkUnsupportedModelWireFields.
	TkUnsupportedModelErrType          = "invalid_request_error"
	TkUnsupportedModelCode             = "model_not_found"
	tkUnsupportedModelAnthropicErrType = "not_found_error"
)

// TkErrorWire names the inbound protocol envelope an error is rendered in.
type TkErrorWire string

const (
	// TkErrorWireOpenAI covers chat/completions, responses, embeddings, images,
	// audio, video, models and OpenAI input_tokens.
	TkErrorWireOpenAI TkErrorWire = "openai"
	// TkErrorWireAnthropic covers /v1/messages and /v1/messages/count_tokens.
	TkErrorWireAnthropic TkErrorWire = "anthropic"
	// TkErrorWireGemini covers native /v1beta generateContent and friends.
	TkErrorWireGemini TkErrorWire = "gemini"
)

// TkUnsupportedModelError is the fully resolved client response for one wire.
type TkUnsupportedModelError struct {
	Wire    TkErrorWire
	Status  int
	Type    string // OpenAI/Anthropic error.type; for Gemini only the ops type
	Code    string // OpenAI error.code; empty for Anthropic and Gemini
	Param   string // OpenAI error.param; empty renders JSON null
	Message string
}

// tkUnsupportedModelErrorFor resolves the response for model on wire with the
// standard TkUnsupportedModelMessage copy.
func tkUnsupportedModelErrorFor(wire TkErrorWire, model string) TkUnsupportedModelError {
	return TkUnsupportedModelErrorWithMessage(wire, TkUnsupportedModelMessage(model))
}

// TkUnsupportedModelErrorWithMessage resolves the response for wire with a
// site-specific diagnostic message (status/type/code still come from here).
func TkUnsupportedModelErrorWithMessage(wire TkErrorWire, message string) TkUnsupportedModelError {
	errType, code := tkUnsupportedModelFieldsForWire(wire)
	return TkUnsupportedModelError{Wire: wire, Status: TkUnsupportedModelStatus, Type: errType, Code: code, Message: message}
}

func tkUnsupportedModelFieldsForWire(wire TkErrorWire) (errType, code string) {
	switch wire {
	case TkErrorWireAnthropic, TkErrorWireGemini:
		return tkUnsupportedModelAnthropicErrType, ""
	default:
		return TkUnsupportedModelErrType, TkUnsupportedModelCode
	}
}

// Body renders the official JSON envelope for e.Wire.
func (e TkUnsupportedModelError) Body() gin.H {
	switch e.Wire {
	case TkErrorWireAnthropic:
		return gin.H{"type": "error", "error": gin.H{"type": e.Type, "message": e.Message}}
	case TkErrorWireGemini:
		return gin.H{"error": gin.H{
			"code":    e.Status,
			"message": e.Message,
			"status":  googleapi.HTTPStatusToGoogleStatus(e.Status),
		}}
	default:
		var param any
		if e.Param != "" {
			param = e.Param
		}
		return gin.H{"error": gin.H{
			"message": e.Message,
			"type":    e.Type,
			"param":   param,
			"code":    e.Code,
		}}
	}
}

// TkErrorWireForPath picks the envelope from the inbound path: Gemini native
// under /v1beta, Anthropic for any /messages route, OpenAI otherwise. It is the
// only copy of this split; the group model allowlist writer maps its result.
func TkErrorWireForPath(path string) TkErrorWire {
	switch {
	case strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta"):
		return TkErrorWireGemini
	case strings.Contains(path, "/messages"):
		return TkErrorWireAnthropic
	default:
		return TkErrorWireOpenAI
	}
}

// TkErrorWireForRequest is TkErrorWireForPath for c's request URL.
func TkErrorWireForRequest(c *gin.Context) TkErrorWire {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return TkErrorWireOpenAI
	}
	return TkErrorWireForPath(c.Request.URL.Path)
}

// TkUnsupportedModelSelectionResponse marks c and returns the
// (status, OpenAI error type, message) triple for handlers whose writers take
// a tuple. The marked writers translate type/code for their own wire.
func TkUnsupportedModelSelectionResponse(c *gin.Context, model string) (int, string, string) {
	TkMarkUnsupportedModelResponse(c)
	return TkUnsupportedModelStatus, TkUnsupportedModelErrType, TkUnsupportedModelMessage(model)
}

// TkWriteUnsupportedModel marks c and writes the official non-streaming body.
// Callers that must stop the gin chain still call c.Abort().
func TkWriteUnsupportedModel(c *gin.Context, wire TkErrorWire, model string) {
	if c == nil {
		return
	}
	e := tkUnsupportedModelErrorFor(wire, model)
	TkMarkUnsupportedModelResponse(c)
	c.JSON(e.Status, e.Body())
}

const tkUnsupportedModelResponseKey = "tk_unsupported_model_response"

// TkMarkUnsupportedModelResponse records that the response being written for c
// is the unsupported-model contract. Shared handler writers (which only carry
// status/type/message) consult it through TkUnsupportedModelWireFields /
// TkUnsupportedModelWireBody so the wire shape stays owned here.
func TkMarkUnsupportedModelResponse(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(tkUnsupportedModelResponseKey, true)
	// Gemini envelopes carry no type; ops reads this to keep the case P3/client.
	SetOpsLocalErrorType(c, TkUnsupportedModelErrType, TkUnsupportedModelCode)
}

func tkIsUnsupportedModelResponse(c *gin.Context, status int) bool {
	return c != nil && status == TkUnsupportedModelStatus && c.GetBool(tkUnsupportedModelResponseKey)
}

// TkUnsupportedModelWireFields returns the owner type/code for wire when c is
// marked and status is the owner status; otherwise errType/code unchanged.
// Used by streaming writers that build their own SSE frames.
func TkUnsupportedModelWireFields(c *gin.Context, wire TkErrorWire, status int, errType, code string) (string, string) {
	if !tkIsUnsupportedModelResponse(c, status) {
		return errType, code
	}
	return tkUnsupportedModelFieldsForWire(wire)
}

// TkUnsupportedModelWireBody returns the official body for wire when c is
// marked and status is the owner status. message is the already-sanitized copy
// the writer would have sent.
func TkUnsupportedModelWireBody(c *gin.Context, wire TkErrorWire, status int, message string) (gin.H, bool) {
	if !tkIsUnsupportedModelResponse(c, status) {
		return nil, false
	}
	return TkUnsupportedModelErrorWithMessage(wire, message).Body(), true
}

// TkIsGatewayUnsupportedModel404 recognizes this owner's 404 when it comes back
// from a nested TokenKey (or sub2api) upstream. The verdict is about that
// upstream group's configuration for one model, so callers must not cool the
// stub account, fail over to treat it as account health, or populate a
// platform-wide negative cache from it. Legacy 400 bodies keep their previous
// handling during rollout.
func TkIsGatewayUnsupportedModel404(statusCode int, body []byte) bool {
	if statusCode != TkUnsupportedModelStatus {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	return strings.HasPrefix(msg, "unsupported model")
}

const (
	TkUnsupportedRequestErrType = "invalid_request_error"
	TkUnsupportedRequestCode    = "unsupported_request"
)

// TkUnsupportedRequestMessage is the client copy when entitled accounts exist
// but none can serve this request. It must not say the service is busy.
func TkUnsupportedRequestMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unsupported request"
	}
	return "Unsupported request for model: " + model
}

// TkUnsupportedModelMessage builds the client-facing message for an unserveable
// model name (single source so path A and path B never drift).
func TkUnsupportedModelMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unsupported model"
	}
	return "Unsupported model: " + model
}

// tkSelectionFailedDueToUnsupportedModel reports whether a failed account
// selection was caused PURELY by "no account serves this model name", as opposed
// to a transient capacity gap.
//
// It is true only when at least one platform-matched candidate lacks the model
// and every account is either model-unsupported or filtered for another
// platform. Any current or future availability category leaves the total
// unmatched, so the caller conservatively falls back to ErrNoAvailableAccounts.
func tkSelectionFailedDueToUnsupportedModel(stats selectionFailureStats) bool {
	return stats.ModelUnsupported > 0 &&
		stats.ModelUnsupported+stats.PlatformFiltered == stats.Total
}

// tkWrapSelectionFailure classifies terminal account-selection failures. It returns:
//   - ErrNoAvailableAccounts as-is when no model was requested;
//   - an Anthropic namespace error only when the resolved scheduling platform is
//     Anthropic; Gemini/Antigravity model names must not be judged as claude-* names;
//   - ErrUnsupportedModel (with the model name + stats) when the failure is
//     purely an unsupported model name (caller fault → handler maps to HTTP 400);
//   - otherwise the original ErrNoAvailableAccounts wrapped with the model + stats
//     (transient capacity → 429), preserving prior behavior.
func tkWrapSelectionFailure(platform, requestedModel string, stats selectionFailureStats) error {
	if requestedModel == "" {
		return ErrNoAvailableAccounts
	}
	if err := tkDeprecatedAnthropicSelectionFailure(requestedModel); err != nil {
		return err
	}
	if platform == PlatformAnthropic {
		if err := tkAnthropicCrossVendorSelectionFailure(requestedModel); err != nil {
			return err
		}
	}
	if tkSelectionFailedDueToUnsupportedModel(stats) {
		return fmt.Errorf("%w: %s (%s)", ErrUnsupportedModel, requestedModel, summarizeSelectionFailureStats(stats))
	}
	if stats.ChannelRestricted > 0 {
		return fmt.Errorf("%w supporting model: %s (channel pricing restriction; %s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
	}
	return fmt.Errorf("%w supporting model: %s (%s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
}

// tkAnthropicCrossVendorSelectionFailure wraps ErrUnsupportedModel when the
// requested model name is outside the Anthropic claude-* namespace (e.g. bare
// "gpt", "opus", deepseek-*). Mirrors tkDeprecatedAnthropicSelectionFailure:
// beats mixed model_unsupported + unschedulable stats and load-batch bare
// ErrNoAvailableAccounts so wrong-model traffic never surfaces routing 429.
//
// Prod 2026-07-07 (user_id=16, api_key_id=121): claude-group direct key hammering
// model=gpt on /v1/messages via load-batch empty-pool returned routing/platform 429
// even though #1255/#1257 fixed retired ids only.
func tkAnthropicCrossVendorSelectionFailure(requestedModel string) error {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	if tkIsForwardableAnthropicModelName(requestedModel) {
		return nil
	}
	// Dual-stack CloudWise anthropic accounts (#94) advertise glm/kimi/minimax/
	// deepseek via model_mapping prefixes. Ingress must not 400 those names
	// before selection; empty-mapping Anthropic OAuth still cannot claim them
	// because isModelSupportedByAccount keeps the claude-* namespace guard.
	if openAICloudwiseRelaySupportsRequestedModel(requestedModel) {
		return nil
	}
	// Dual-stack tokensea anthropic accounts (#93) advertise the mapped
	// subset of agent.tokensea.ai via model_mapping. Ingress
	// must not 400 those exact IDs before selection; official Anthropic
	// OAuth/APIKey still cannot claim them because isModelSupportedByAccount
	// keeps the claude-* namespace guard unless IsAnthropicTokenseaRelay.
	if tokenseaRelaySupportsRequestedModel(requestedModel) {
		return nil
	}
	if normalized := claude.NormalizeModelID(requestedModel); normalized != "" && normalized != requestedModel {
		if tkIsForwardableAnthropicModelName(normalized) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s (anthropic namespace)", ErrUnsupportedModel, requestedModel)
}

// TkIsAnthropicCrossVendorModelName reports whether reqModel is outside the
// Anthropic claude-* namespace. Exported for handler ingress pre-checks.
func TkIsAnthropicCrossVendorModelName(requestedModel string) bool {
	return tkAnthropicCrossVendorSelectionFailure(requestedModel) != nil
}

func tkIsForwardableAnthropicModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true // empty model name is out of this guard's scope (handled elsewhere)
	}
	return strings.HasPrefix(m, "claude-")
}

func tkIsForwardableOpenAIModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	m = strings.TrimPrefix(m, "openai/")
	m = strings.ReplaceAll(m, " ", "")
	if m == "" {
		return true
	}
	openAIPrefixes := []string{
		"gpt",
		"o1", "o2", "o3", "o4", "o5", "o6", "o7", "o8", "o9",
		"chatgpt",
		"codex",
		"text-embedding",
		"text-moderation",
		"dall-e",
		"tts",
		"whisper",
		"omni",
		"computer-use",
		"sora",
	}
	for _, prefix := range openAIPrefixes {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// TkErrorWireForShape maps a universal-routing endpoint shape to its envelope.
func TkErrorWireForShape(shape UniversalShape) TkErrorWire {
	switch shape {
	case ShapeGemini:
		return TkErrorWireGemini
	case ShapeAnthropicMessages, ShapeAnthropicCountTokens:
		return TkErrorWireAnthropic
	default:
		return TkErrorWireOpenAI
	}
}

// tkWriteAnthropicUnsupportedModel is the service-layer Anthropic emit used by
// the upstream-404 translation and the not-found negative cache. It goes
// through tkWriteAnthropicErrorResponse so a committed SSE heartbeat still
// gets a well-formed error event.
func tkWriteAnthropicUnsupportedModel(c *gin.Context, model string) {
	e := tkUnsupportedModelErrorFor(TkErrorWireAnthropic, model)
	TkMarkUnsupportedModelResponse(c)
	tkWriteAnthropicError(c, e.Status, e.Type, e.Message)
}

// TkUnsupportedModelSelectionResponseWithMessage is TkUnsupportedModelSelectionResponse
// with a site-specific diagnostic message (group mapping gap, unknown image id).
func TkUnsupportedModelSelectionResponseWithMessage(c *gin.Context, message string) (int, string, string) {
	TkMarkUnsupportedModelResponse(c)
	return TkUnsupportedModelStatus, TkUnsupportedModelErrType, message
}
