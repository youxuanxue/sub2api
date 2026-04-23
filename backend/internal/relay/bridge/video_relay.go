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
	"github.com/gin-gonic/gin"
)

// TaskSubmitOutcome is the result of a video task submission via the New API
// task adaptor. The bridge does NOT touch new-api's GORM model.Task table —
// upstream task ID + minimal context are returned to the caller, which is
// responsible for persisting them (e.g. Redis-backed VideoTaskRegistry) so
// later /v1/video/generations/:task_id fetches can be routed back to the same
// account.
type TaskSubmitOutcome struct {
	UpstreamTaskID string
	UpstreamModel  string
	OriginModel    string
	ChannelType    int
	BaseURL        string
	APIKey         string
	Action         string
	RawResponse    []byte
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
	OriginModel    string
}

// VideoFetchOutcome holds the upstream raw response. The handler is responsible
// for serializing it back to the client; we deliberately avoid materializing
// it as a typed dto.OpenAIVideo inside the bridge to stay compatible with the
// New API contract evolution (volcengine etc.).
type VideoFetchOutcome struct {
	RawResponse []byte
	Status      string
	Progress    string
	URL         string
	Duration    time.Duration
	OriginModel string
}

// errVideoUnsupportedChannel is returned when no New API task adaptor is
// registered for the account's channel_type.
type errVideoUnsupportedChannel struct {
	ChannelType int
}

func (e *errVideoUnsupportedChannel) Error() string {
	return fmt.Sprintf("video generation not supported for channel_type=%d", e.ChannelType)
}

// DispatchVideoSubmit runs the New API task adaptor for POST /v1/video/generations
// (and the OpenAI-compat alias POST /v1/videos). It performs:
//
//   1) populate context keys + body storage
//   2) parse TaskSubmitReq into Gin context (so adaptor.GetTaskRequest works)
//   3) resolve the task adaptor by channel_type
//   4) Init / Validate / BuildRequest / DoRequest / DoResponse
//
// It deliberately skips:
//
//   - PreConsumeBilling / SettleBilling (TokenKey owns billing separately)
//   - model.GenerateTaskID / model.Task.Insert (TokenKey owns the registry)
//   - ResolveOriginTask (remix not yet supported in TK)
func DispatchVideoSubmit(_ context.Context, c *gin.Context, in ChannelContextInput, body []byte) (*TaskSubmitOutcome, *types.NewAPIError) {
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

	platform := newapiconstant.TaskPlatform(strconv.Itoa(in.ChannelType))
	adaptor := newapirelay.GetTaskAdaptor(platform)
	if adaptor == nil {
		return nil, types.NewError(&errVideoUnsupportedChannel{ChannelType: in.ChannelType}, types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	// InitChannelMeta materializes the embedded *ChannelMeta from gin context;
	// it MUST run before any field on ChannelMeta (UpstreamModelName etc.) is
	// read or written. Skipping it caused a nil pointer deref in early dev.
	relayInfo.InitChannelMeta(c)
	relayInfo.UpstreamModelName = req.Model
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

	upstreamModel := relayInfo.UpstreamModelName
	if upstreamModel == "" {
		upstreamModel = req.Model
	}

	return &TaskSubmitOutcome{
		UpstreamTaskID: upstreamTaskID,
		UpstreamModel:  upstreamModel,
		OriginModel:    req.Model,
		ChannelType:    in.ChannelType,
		BaseURL:        in.BaseURL,
		APIKey:         in.APIKey,
		Action:         relayInfo.Action,
		RawResponse:    taskData,
		Duration:       dur,
	}, nil
}

// DispatchVideoFetch resolves a single video task status by calling the
// adaptor's FetchTask. It returns the upstream raw bytes plus a coarse status
// snapshot extracted from ParseTaskResult so the handler can decide whether to
// 404 / 200 / etc.
func DispatchVideoFetch(_ context.Context, _ *gin.Context, in VideoFetchInput) (*VideoFetchOutcome, *types.NewAPIError) {
	ensureNewAPIDeps()
	if strings.TrimSpace(in.UpstreamTaskID) == "" {
		return nil, types.NewError(errors.New("upstream task id is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	platform := newapiconstant.TaskPlatform(strconv.Itoa(in.ChannelType))
	adaptor := newapirelay.GetTaskAdaptor(platform)
	if adaptor == nil {
		return nil, types.NewError(&errVideoUnsupportedChannel{ChannelType: in.ChannelType}, types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}

	baseURL := in.BaseURL
	if baseURL == "" && in.ChannelType >= 0 && in.ChannelType < len(newapiconstant.ChannelBaseURLs) {
		baseURL = newapiconstant.ChannelBaseURLs[in.ChannelType]
	}

	start := time.Now()
	resp, err := adaptor.FetchTask(baseURL, in.APIKey, map[string]any{
		"task_id": in.UpstreamTaskID,
	}, "")
	dur := time.Since(start)
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

	out := &VideoFetchOutcome{
		RawResponse: body,
		Duration:    dur,
		OriginModel: in.OriginModel,
	}
	if info, parseErr := adaptor.ParseTaskResult(body); parseErr == nil && info != nil {
		out.Status = string(info.Status)
		out.Progress = info.Progress
		out.URL = info.Url
	}
	return out, nil
}

// taskErrorToNewAPIError converts the new-api dto.TaskError into a NewAPIError
// shape that the bridge layer expects.
func taskErrorToNewAPIError(taskErr *dto.TaskError) *types.NewAPIError {
	if taskErr == nil {
		return nil
	}
	status := taskErr.StatusCode
	if status == 0 {
		status = http.StatusBadGateway
	}
	wrapped := taskErr.Error
	if wrapped == nil {
		wrapped = errors.New(taskErr.Message)
	}
	code := types.ErrorCodeBadResponseStatusCode
	if taskErr.Code != "" {
		code = types.ErrorCode(taskErr.Code)
	}
	return types.NewErrorWithStatusCode(wrapped, code, status, types.ErrOptionWithSkipRetry())
}

// IsVideoSupportedChannelType reports whether new-api's task-adaptor registry
// has an entry for this channel type. Used by the route layer / settings to
// pre-flight requests before queuing them.
func IsVideoSupportedChannelType(channelType int) bool {
	if channelType <= 0 {
		return false
	}
	platform := newapiconstant.TaskPlatform(strconv.Itoa(channelType))
	return newapirelay.GetTaskAdaptor(platform) != nil
}

// _ keeps the channel package referenced even when only the registry helpers
// above are exported — guards against a future refactor accidentally dropping
// the dependency that downstream task-adaptor type assertions rely on.
var _ = channel.OpenAIVideoConverter(nil)
