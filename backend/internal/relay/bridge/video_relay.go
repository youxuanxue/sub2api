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
	"github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine"
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
}

// VideoFetchOutcome holds the upstream raw response and the parsed status
// snapshot the handler needs to decide whether to expire the registry record.
// The raw bytes pass through to the client untouched so SDKs see the same
// body shape new-api would have returned for this channel type.
type VideoFetchOutcome struct {
	RawResponse []byte
	Status      string
}

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

	adaptor := taskAdaptorForChannel(in.ChannelType)
	if adaptor == nil {
		return nil, errUnsupportedChannel(in.ChannelType)
	}
	// InitChannelMeta materializes the embedded *ChannelMeta from gin context;
	// it MUST run before any field on ChannelMeta (UpstreamModelName etc.) is
	// read or written. Skipping it caused a nil pointer deref in early dev.
	relayInfo.InitChannelMeta(c)
	relayInfo.UpstreamModelName = req.Model
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
		bodyBytes, _ := io.ReadAll(resp.Body)
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
	adaptor := taskAdaptorForChannel(in.ChannelType)
	if adaptor == nil {
		return nil, errUnsupportedChannel(in.ChannelType)
	}

	baseURL := in.BaseURL
	if baseURL == "" && in.ChannelType >= 0 && in.ChannelType < len(newapiconstant.ChannelBaseURLs) {
		baseURL = newapiconstant.ChannelBaseURLs[in.ChannelType]
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadResponseBodyFailed, types.ErrOptionWithSkipRetry())
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

// taskAdaptorForChannel returns the new-api task adaptor registered for this
// channel type, or nil. DispatchVideoSubmit and DispatchVideoFetch own the
// bridge-local lookup; external preflight callers MUST use engine-level truth
// so capability semantics stay centralized outside the bridge package.
func taskAdaptorForChannel(channelType int) channel.TaskAdaptor {
	if channelType <= 0 {
		return nil
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
