package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	newapirelay "github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	newapihelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
)

// TaskSubmitOutcome is the result of a video task submission via the New API
// task adaptor. The bridge does NOT touch new-api's GORM model.Task table —
// it returns the upstream task ID + model names + the routing snapshot
// (channel_type + base_url + api_key as resolved by the bridge for this
// submit). The routing snapshot lets the caller persist what was actually
// dispatched to (which may differ from a future re-resolution of the same
// Account if credentials rotate before the user polls). Duration is exposed
// so the handler can record latency in usage_logs without timing twice.
//
// IMPORTANT response-write ordering: new-api task adaptors (doubao, jimeng,
// vidu, …) write the OpenAI-Video-shaped JSON response to gin.Context
// inside DoResponse, embedding `relayInfo.PublicTaskID` as the task id.
// The handler MUST therefore (a) pre-generate the public task id and pass
// it into PublicTaskID below so the adaptor stamps it on the wire, and
// (b) NOT call c.JSON again afterwards — the response is already on
// the writer when DispatchVideoSubmit returns.
type TaskSubmitOutcome struct {
	PublicTaskID   string
	UpstreamTaskID string
	UpstreamModel  string
	OriginModel    string
	ChannelType    int
	BaseURL        string
	APIKey         string
	Duration       time.Duration
}

// VideoFetchInput identifies which upstream account+task the fetch should
// target. These values must be re-populated from a registry lookup before the
// caller invokes DispatchVideoFetch — there is no stateful new-api model.Task.
type VideoFetchInput struct {
	UpstreamTaskID string
	ChannelType    int
	BaseURL        string
	APIKey         string
	// Platform + AccountID support the grok-native video poll path (platform=grok,
	// channel_type=0), which does NOT go through the new-api task adaptor. The
	// bridge fetch ignores both; the TK service layer branches on Platform=="grok"
	// to a native poll and re-resolves a fresh OAuth Bearer via AccountID (the
	// pinned APIKey may be a rotated/stale grok token by poll time). Empty/zero
	// for the bridge (channel_type>0) path — fully backward compatible.
	Platform  string
	AccountID int64
}

// VideoFetchOutcome holds the upstream raw response and the parsed status
// snapshot the handler needs to decide whether to expire the registry record.
// The raw bytes pass through to the client untouched so SDKs see the same
// body shape new-api would have returned for this channel type.
type VideoFetchOutcome struct {
	RawResponse []byte
	Status      string
}

// videoSubmitErrorBodyMaxBytes bounds untrusted upstream diagnostics before they
// are copied into a typed error. Successful task bodies are parsed by the adaptor.
const videoSubmitErrorBodyMaxBytes int64 = 512 << 10

// videoFetchResponseMaxBytes bounds task-poll response bodies. Some upstreams
// return terminal video bytes as inline base64 JSON; TokenKey should hand that
// result to the client once, not accept unbounded media into memory.
const videoFetchResponseMaxBytes int64 = 80 << 20

var errVideoFetchResponseTooLarge = errors.New("upstream task fetch response too large")

// DispatchVideoSubmit runs the New API task adaptor for POST /v1/video/generations
// (and the OpenAI-compat alias POST /v1/videos). It performs:
//
//  1. populate context keys + body storage
//  2. parse TaskSubmitReq into Gin context (so adaptor.GetTaskRequest works)
//  3. resolve the task adaptor by channel_type
//  4. Init / Validate / BuildRequest / DoRequest / DoResponse
//
// It deliberately skips:
//
//   - PreConsumeBilling / SettleBilling (TokenKey owns billing separately)
//   - model.GenerateTaskID / model.Task.Insert (TokenKey owns the registry)
//   - ResolveOriginTask (remix not yet supported in TK)
//
// The adaptor's DoResponse writes the OpenAI-Video JSON to c with
// `relayInfo.PublicTaskID` as the task id; the caller MUST pass a
// pre-generated, registry-stable id via publicTaskID so that response
// matches the registry record. The caller MUST NOT write c.JSON again
// after this returns — the response body has already been sent.
func DispatchVideoSubmit(_ context.Context, c *gin.Context, in ChannelContextInput, publicTaskID string, body []byte) (*TaskSubmitOutcome, *types.NewAPIError) {
	if strings.TrimSpace(publicTaskID) == "" {
		return nil, types.NewError(errors.New("public task id is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	normalizedBody, err := normalizeVideoSubmitBodyForTaskAdaptor(body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	body = normalizedBody
	ensureNewAPIDeps()
	if err := installBodyStorage(c, body); err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}

	// Parse into TaskSubmitReq up-front so we can echo back the model name and
	// avoid relying on adaptor side effects.
	var req relaycommon.TaskSubmitReq
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, types.NewError(errors.New("model is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	PopulateContextKeys(c, in)
	SetOriginalModel(c, req.Model)
	if c.GetString(common.RequestIdKey) == "" {
		SetRequestID(c, NewRequestID())
	}
	// Preserve the post-parse body so adaptors that re-read can still see it.
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithSkipRetry())
	}
	relayInfo.OriginModelName = req.Model
	if relayInfo.RelayMode == relayconstant.RelayModeUnknown {
		relayInfo.RelayMode = relayconstant.RelayModeVideoSubmit
	}

	adaptor := taskAdaptorForChannel(in.ChannelType, in.BaseURL)
	if adaptor == nil {
		return nil, errUnsupportedChannel(in.ChannelType)
	}
	// InitChannelMeta materializes the embedded *ChannelMeta from gin context;
	// it MUST run before any field on ChannelMeta (UpstreamModelName etc.) is
	// read or written. Skipping it caused a nil pointer deref in early dev.
	relayInfo.InitChannelMeta(c)
	relayInfo.UpstreamModelName = req.Model
	// Apply the account's credentials.model_mapping, exactly as the four sibling
	// bridge relays do (text/responses/embedding/image all call this right after
	// InitChannelMeta). Video was the only relay missing it, so the requested
	// model was forwarded verbatim and every account whose upstream names its
	// SKUs differently was unreachable — e.g. XRToken serves Ark Seedance as
	// `volcengine/doubao-seedance-*` and rejected the bare Ark id.
	//
	// Two invariants this must preserve:
	//
	//   - BILLING KEY IS UNCHANGED. relayInfo.OriginModelName (set above from
	//     req.Model) is what TaskSubmitOutcome.OriginModel returns and what the
	//     handler bills on. ModelMappedHelper only rewrites UpstreamModelName, so
	//     the overlay price key stays the client-facing Ark id. Rewriting the
	//     request body instead would have moved the billing key to the upstream
	//     name and billed $0 for an id absent from the overlay.
	//   - IDENTITY MAPPINGS STAY A NO-OP. For `X: X` the helper's cycle check
	//     reports IsModelMapped=false and leaves UpstreamModelName alone, so
	//     existing Ark accounts (tk_033/tk_056 write identity whitelists) behave
	//     exactly as before.
	//
	// request is nil on purpose: TaskSubmitReq does not implement dto.Request
	// (no SetModelName), and the task adaptors already substitute the mapped name
	// themselves from info.IsModelMapped/UpstreamModelName in BuildRequestBody.
	if err := newapihelper.ModelMappedHelper(c, relayInfo, nil); err != nil {
		return nil, types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	// Seed the public task id so adaptor.DoResponse stamps it on the wire.
	// Without this every adaptor would write an empty / random id, making
	// the GET /v1/videos/:task_id response inconsistent with the POST.
	relayInfo.PublicTaskID = publicTaskID
	adaptor.Init(relayInfo)

	if taskErr := adaptor.ValidateRequestAndSetAction(c, relayInfo); taskErr != nil {
		return nil, taskErrorToNewAPIError(taskErr)
	}

	requestBody, err := adaptor.BuildRequestBody(c, relayInfo)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	start := time.Now()
	resp, err := adaptor.DoRequest(c, relayInfo, requestBody)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	dur := time.Since(start)
	if resp == nil {
		return nil, types.NewError(errors.New("empty upstream response"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, videoSubmitErrorBodyMaxBytes))
		_ = resp.Body.Close()
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("upstream task submit failed: %s", strings.TrimSpace(string(bodyBytes))),
			types.ErrorCodeBadResponseStatusCode,
			resp.StatusCode,
			types.ErrOptionWithSkipRetry(),
		)
	}

	upstreamTaskID, taskData, taskErr := adaptor.DoResponse(c, resp, relayInfo)
	if taskErr != nil {
		return nil, taskErrorToNewAPIError(taskErr)
	}
	if strings.TrimSpace(upstreamTaskID) == "" {
		return nil, types.NewError(errors.New("empty upstream task id"), types.ErrorCodeBadResponseStatusCode, types.ErrOptionWithSkipRetry())
	}

	// UpstreamModelName was just set above to req.Model on the freshly
	// initialised ChannelMeta; an adaptor that legitimately rewrites it
	// (model_mapping) updates the same field in place. Direct read.
	// taskData (raw upstream response) is intentionally discarded — the
	// adaptor's DoResponse already wrote the OpenAI-Video-shaped JSON
	// straight to the gin context for the synchronous submit response.
	_ = taskData
	return &TaskSubmitOutcome{
		PublicTaskID:   publicTaskID,
		UpstreamTaskID: upstreamTaskID,
		UpstreamModel:  relayInfo.UpstreamModelName,
		OriginModel:    req.Model,
		ChannelType:    in.ChannelType,
		BaseURL:        in.BaseURL,
		APIKey:         in.APIKey,
		Duration:       dur,
	}, nil
}

// normalizeVideoSubmitBodyForTaskAdaptor aligns TokenKey's public video fields
// with the pinned new-api TaskSubmitReq, whose provider-specific options live in
// metadata. Billing reads the same top-level values before dispatch, so this
// translation is required to keep the charged tier and generated tier identical.
func normalizeVideoSubmitBodyForTaskAdaptor(body []byte) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse video submit body: %w", err)
	}

	resolution, hasResolution, err := rawJSONStringAliases(raw, "resolution", "size")
	if err != nil {
		return nil, err
	}
	generateAudio, hasGenerateAudio, err := rawJSONBoolAliases(raw, "generateAudio", "generate_audio")
	if err != nil {
		return nil, err
	}
	metadata, err := decodeTaskMetadata(raw["metadata"])
	if err != nil {
		if !hasResolution && !hasGenerateAudio {
			return body, nil
		}
		return nil, err
	}
	metadataResolution, hasMetadataResolution, err := metadataJSONStringAliases(metadata, "resolution", "size")
	if err != nil {
		return nil, err
	}
	metadataGenerateAudio, hasMetadataGenerateAudio, err := metadataJSONBoolAliases(metadata, "generateAudio", "generate_audio")
	if err != nil {
		return nil, err
	}
	if !hasResolution {
		resolution = metadataResolution
		hasResolution = hasMetadataResolution
	}
	if !hasGenerateAudio {
		generateAudio = metadataGenerateAudio
		hasGenerateAudio = hasMetadataGenerateAudio
	}
	if !hasResolution && !hasGenerateAudio {
		return body, nil
	}
	if hasResolution {
		normalizedResolution, ok := newapiintegration.NormalizeVideoTaskResolution(resolution)
		if !ok {
			return nil, fmt.Errorf("video resolution %q is unsupported", resolution)
		}
		metadata["resolution"] = normalizedResolution
	}
	if hasGenerateAudio {
		// Veo uses camelCase; Seedance uses snake_case.
		metadata["generateAudio"] = generateAudio
		metadata["generate_audio"] = generateAudio
	}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode video metadata: %w", err)
	}
	raw["metadata"] = metadataRaw
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode video submit body: %w", err)
	}
	return normalized, nil
}

func rawJSONStringAliases(raw map[string]json.RawMessage, keys ...string) (string, bool, error) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return "", false, fmt.Errorf("video %s must be a string", key)
		}
		decoded = strings.TrimSpace(decoded)
		if decoded == "" {
			return "", false, fmt.Errorf("video %s must not be empty", key)
		}
		return decoded, true, nil
	}
	return "", false, nil
}

func rawJSONBoolAliases(raw map[string]json.RawMessage, keys ...string) (bool, bool, error) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var decoded bool
		if err := json.Unmarshal(value, &decoded); err != nil {
			return false, false, fmt.Errorf("video %s must be a boolean", key)
		}
		return decoded, true, nil
	}
	return false, false, nil
}

func metadataJSONStringAliases(metadata map[string]any, keys ...string) (string, bool, error) {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		decoded, ok := value.(string)
		if !ok || strings.TrimSpace(decoded) == "" {
			return "", false, fmt.Errorf("video metadata.%s must be a non-empty string", key)
		}
		return strings.TrimSpace(decoded), true, nil
	}
	return "", false, nil
}

func metadataJSONBoolAliases(metadata map[string]any, keys ...string) (bool, bool, error) {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		decoded, ok := value.(bool)
		if !ok {
			return false, false, fmt.Errorf("video metadata.%s must be a boolean", key)
		}
		return decoded, true, nil
	}
	return false, false, nil
}

func decodeTaskMetadata(raw json.RawMessage) (map[string]any, error) {
	metadata := make(map[string]any)
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return metadata, nil
	}
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, fmt.Errorf("video metadata must be an object or JSON object string")
		}
		raw = []byte(encoded)
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("video metadata must be an object or JSON object string")
	}
	return metadata, nil
}

// DispatchVideoFetch resolves a single video task status by calling the
// adaptor's FetchTask. It returns the upstream raw bytes plus a coarse status
// snapshot extracted from ParseTaskResult so the handler can decide whether
// to expire the registry entry. baseURL falls back to the channel-type
// default only when the registry record was saved with an empty base_url
// (legacy / migrated tasks).
func DispatchVideoFetch(_ context.Context, _ *gin.Context, in VideoFetchInput) (*VideoFetchOutcome, *types.NewAPIError) {
	ensureNewAPIDeps()
	if strings.TrimSpace(in.UpstreamTaskID) == "" {
		return nil, types.NewError(errors.New("upstream task id is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	// Resolve base_url BEFORE adaptor selection: which adaptor serves this
	// channel_type can depend on the base (XRToken shares ch54 with official
	// Ark, distinguished only by host), so selecting first would pick the Ark
	// adaptor for a registry record that stored an empty base and then fell
	// back to the Ark default.
	baseURL := in.BaseURL
	if baseURL == "" && in.ChannelType >= 0 && in.ChannelType < len(newapiconstant.ChannelBaseURLs) {
		baseURL = newapiconstant.ChannelBaseURLs[in.ChannelType]
	}

	adaptor := taskAdaptorForChannel(in.ChannelType, baseURL)
	if adaptor == nil {
		return nil, errUnsupportedChannel(in.ChannelType)
	}

	resp, err := adaptor.FetchTask(baseURL, in.APIKey, map[string]any{
		"task_id": in.UpstreamTaskID,
	}, "")
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	if resp == nil {
		return nil, types.NewError(errors.New("empty upstream fetch response"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readVideoFetchResponseBodyForAdaptor(adaptor, resp.Body)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("upstream task fetch failed: %s", strings.TrimSpace(string(body))),
			types.ErrorCodeBadResponseStatusCode,
			resp.StatusCode,
			types.ErrOptionWithSkipRetry(),
		)
	}

	out := &VideoFetchOutcome{RawResponse: body}
	if info, parseErr := adaptor.ParseTaskResult(body); parseErr == nil && info != nil {
		out.Status = string(info.Status)
	}
	return out, nil
}

// videoFetchResponseSanitizer is implemented by task adaptors whose upstream
// speaks a dialect that must not reach the client. Kept as a narrow optional
// interface rather than a branch on base_url so the rule stays in the variant's
// own file (see video_relay_tk_xrtoken.go) and adaptors without a dialect need
// no code at all.
type videoFetchResponseSanitizer interface {
	sanitizeFetchResponse(body []byte) []byte
}

// readVideoFetchResponseBodyForAdaptor reads the bounded poll body and gives a
// variant adaptor one chance to normalize upstream-dialect fields back to the
// client-facing contract before anything is returned.
//
// The two steps are fused into ONE function on purpose. The handler hands
// VideoFetchOutcome.RawResponse to the client verbatim, so a dialect field that
// survives here reaches the client — and a separate, skippable "sanitize" step at
// the call site is exactly the kind of line a later refactor drops silently while
// every test still passes. Fusing them means the only way to skip sanitizing is
// to stop reading the body at all, which fails loudly.
//
// Sanitizing deliberately happens AFTER the bounded read rather than inside
// FetchTask: videoFetchResponseMaxBytes is enforced here, and some upstreams
// return terminal video bytes inline, so a rewrite that consumed the stream
// earlier (FetchTask hands back an *http.Response) would bypass that bound and
// pull unbounded media into memory.
func readVideoFetchResponseBodyForAdaptor(adaptor channel.TaskAdaptor, r io.Reader) ([]byte, error) {
	body, err := readVideoFetchResponseBody(r)
	if err != nil {
		return nil, err
	}
	if variant, ok := adaptor.(videoFetchResponseSanitizer); ok {
		body = variant.sanitizeFetchResponse(body)
	}
	return body, nil
}

func readVideoFetchResponseBody(r io.Reader) ([]byte, error) {
	return readVideoFetchResponseBodyLimited(r, videoFetchResponseMaxBytes)
}

func readVideoFetchResponseBodyLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if r == nil {
		return nil, errors.New("response body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = videoFetchResponseMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", errVideoFetchResponseTooLarge, maxBytes)
	}
	return body, nil
}

// taskAdaptorForChannel returns the new-api task adaptor registered for this
// channel type, or nil. DispatchVideoSubmit and DispatchVideoFetch own the
// bridge-local lookup; external preflight callers MUST use engine-level truth
// so capability semantics stay centralized outside the bridge package.
//
// baseURL selects among variants that share a channel_type. Today that is
// XRToken, an ARK-compatible reseller reached on ChannelTypeDoubaoVideo whose
// task paths differ from official Ark by one middle path segment — see
// video_relay_tk_xrtoken.go. Dispatching on the sentinel base_url (rather than
// minting a TK-private channel_type) keeps this a pure bridge-local concern:
// engine capability, route registration and the admin channel catalog all keep
// treating the account as ch54, exactly as they do for the ch45 Agent Plan and
// ch46 Qianfan sentinels.
//
// An empty baseURL falls through to the upstream adaptor, which is the correct
// default: callers that have no base_url (legacy registry rows) get official
// Ark behavior, unchanged from before this override existed.
func taskAdaptorForChannel(channelType int, baseURL string) channel.TaskAdaptor {
	if channelType <= 0 {
		return nil
	}
	if newapiintegration.IsXRTokenBaseURL(channelType, baseURL) {
		return newXRTokenTaskAdaptor()
	}
	if newapiintegration.IsFMGoBaseURL(channelType, baseURL) {
		return newFMGoTaskAdaptor()
	}
	platform := newapiconstant.TaskPlatform(strconv.Itoa(channelType))
	return newapirelay.GetTaskAdaptor(platform)
}

// IsVideoSupportedChannelType preserves the bridge's load-bearing exported
// surface while delegating capability truth to the engine registry.
func IsVideoSupportedChannelType(channelType int) bool {
	return engine.IsVideoSupportedChannelType(channelType)
}

// errUnsupportedChannel is the canonical error for "no task adaptor for this
// channel type". Inlined as a plain fmt.Errorf because no caller does
// errors.As on it — the previous typed struct was dead code.
func errUnsupportedChannel(channelType int) *types.NewAPIError {
	return types.NewError(
		fmt.Errorf("video generation not supported for channel_type=%d", channelType),
		types.ErrorCodeInvalidApiType,
		types.ErrOptionWithSkipRetry(),
	)
}

// taskErrorToNewAPIError converts the new-api dto.TaskError into a NewAPIError
// shape that the bridge layer expects.
//
// taskErr.Error is always non-nil when this is called: every TaskError that
// reaches an adaptor's DoResponse is built by service.TaskErrorWrapper /
// TaskErrorWrapperLocal / relaycommon.createTaskError / TaskErrorFromAPIError,
// and all four constructors call `err.Error()` on a non-nil err during
// construction. (The two `&dto.TaskError{...}` literals in
// new-api/controller/relay.go are written via c.JSON directly and never reach
// our path.) We therefore do not synthesise a fallback error from .Message.
func taskErrorToNewAPIError(taskErr *dto.TaskError) *types.NewAPIError {
	if taskErr == nil {
		return nil
	}
	status := taskErr.StatusCode
	if status == 0 {
		status = http.StatusBadGateway
	}
	code := types.ErrorCodeBadResponseStatusCode
	if taskErr.Code != "" {
		code = types.ErrorCode(taskErr.Code)
	}
	return types.NewErrorWithStatusCode(taskErr.Error, code, status, types.ErrOptionWithSkipRetry())
}
