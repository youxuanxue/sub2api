package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	newapichannel "github.com/QuantumNous/new-api/relay/channel"
	taskdoubao "github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	newapiservice "github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// TokenKey: FMGo (feimiao) video task adaptor.
//
// Official feimiao-v2 / feimiao-v2-fast dialect is async chat completions
// (POST /v1/chat/completions + GET /v1/tasks/{id}), not OpenAI /v1/videos.
// TokenKey clients still send official Ark ids plus resolution/duration.
// Identification is channel_type + base_url only — never supplier_source_id.
type fmgoTaskAdaptor struct {
	*taskdoubao.TaskAdaptor
	baseURL string
}

func newFMGoTaskAdaptor() *fmgoTaskAdaptor {
	return &fmgoTaskAdaptor{TaskAdaptor: &taskdoubao.TaskAdaptor{}}
}

func (a *fmgoTaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info != nil {
		a.baseURL = newapiintegration.NormalizeFMGoBaseURL(info.ChannelBaseUrl)
	}
	a.TaskAdaptor.Init(info)
}

func (a *fmgoTaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	if a.baseURL == "" {
		return "", fmt.Errorf("fmgo video submit: empty base_url")
	}
	return a.baseURL + newapiintegration.FMGoChatCompletionsPath, nil
}

func (a *fmgoTaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	if err := a.TaskAdaptor.BuildRequestHeader(c, req, info); err != nil {
		return err
	}
	req.Header.Set("Prefer", "respond-async")
	return nil
}

func (a *fmgoTaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	resp, err := newapichannel.DoTaskApiRequest(a, c, info, requestBody)
	if err != nil || resp == nil {
		return resp, err
	}
	// Vendor guide: create returns HTTP 202. DispatchVideoSubmit only accepts 200.
	if resp.StatusCode == http.StatusAccepted {
		resp.StatusCode = http.StatusOK
		resp.Status = http.StatusText(http.StatusOK)
	}
	return resp, nil
}

func (a *fmgoTaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}
	base := newapiintegration.NormalizeFMGoBaseURL(baseUrl)
	if base == "" {
		return nil, fmt.Errorf("fmgo video fetch: empty base_url")
	}
	uri := fmt.Sprintf("%s%s/%s", base, newapiintegration.FMGoTaskPathPrefix, taskID)
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := newapiservice.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("fmgo video fetch: new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *fmgoTaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	orig := fmgoOriginalSubmitBody(c)
	wireModel := gjson.GetBytes(orig, "model").String()
	if wireModel == "" && info != nil {
		wireModel = info.OriginModelName
	}
	client := fmgoSeedanceClientFromRelay(info, wireModel)
	resolution, duration, durErr := fmgoVideoParamsFromSubmit(c, orig)
	if durErr != nil {
		return nil, durErr
	}
	upstream, skuErr := newapiintegration.FMGoSeedanceUpstreamSKU(client, resolution, duration)
	if skuErr != nil {
		return nil, skuErr
	}
	if info != nil {
		info.UpstreamModelName = upstream
	}
	if resolution == "" {
		resolution = newapiintegration.FMGoDefaultResolution
	}
	if duration == 0 {
		duration = newapiintegration.FMGoDefaultDuration
	}
	payload, err := fmgoChatCompletionsBody(upstream, fmgoPromptFromSubmit(c, orig), fmgoImagesFromSubmit(c, orig), resolution, duration, fmgoAspectRatioFromSubmit(c, orig))
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(payload), nil
}

func (a *fmgoTaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	info := &relaycommon.TaskInfo{Code: 0}
	status := strings.ToLower(firstNonEmptyJSONString(respBody, "status", "task.status"))
	videoURL := firstNonEmptyJSONString(respBody, "result.url", "content.video_url")
	reason := firstNonEmptyJSONString(respBody, "error.message", "task.error", "message")
	switch status {
	case "queued", "pending":
		info.Status = model.TaskStatusQueued
		info.Progress = "10%"
	case "in_progress", "processing", "running":
		info.Status = model.TaskStatusInProgress
		info.Progress = "50%"
	case "completed", "succeeded", "success":
		info.Status = model.TaskStatusSuccess
		info.Progress = "100%"
		info.Url = videoURL
	case "failed", "failure", "error":
		info.Status = model.TaskStatusFailure
		info.Progress = "100%"
		info.Reason = reason
	default:
		info.Status = model.TaskStatusInProgress
		info.Progress = "30%"
	}
	return info, nil
}

func fmgoChatCompletionsBody(model, prompt string, images []string, resolution string, duration int, aspectRatio string) ([]byte, error) {
	message := map[string]any{"role": "user"}
	if len(images) == 0 {
		message["content"] = prompt
	} else {
		parts := []map[string]any{{"type": "text", "text": prompt}}
		for _, imageURL := range images {
			imageURL = strings.TrimSpace(imageURL)
			if imageURL == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": imageURL},
			})
		}
		message["content"] = parts
	}
	return json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]any{
			message,
		},
		"generationConfig": map[string]any{
			"videoConfig": map[string]any{
				"duration":    duration,
				"aspectRatio": aspectRatio,
				"resolution":  resolution,
			},
		},
		"async": true,
	})
}

// fmgoSeedanceClientFromRelay prefers OriginModelName: production mapping remaps
// the official client to a probe-anchor SKU, which must not freeze the runtime SKU.
func fmgoSeedanceClientFromRelay(info *relaycommon.RelayInfo, wireModel string) string {
	if info != nil && newapiintegration.IsFMGoSeedanceClient(info.OriginModelName) {
		return strings.TrimSpace(info.OriginModelName)
	}
	if newapiintegration.IsFMGoSeedanceClient(wireModel) {
		return strings.TrimSpace(wireModel)
	}
	if info != nil && strings.TrimSpace(info.OriginModelName) != "" {
		return strings.TrimSpace(info.OriginModelName)
	}
	return strings.TrimSpace(wireModel)
}

func (a *fmgoTaskAdaptor) sanitizeFetchResponse(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	current := gjson.GetBytes(body, "model")
	if !current.Exists() {
		return body
	}
	model := current.String()
	if !strings.HasPrefix(model, "feimiao-v2-") {
		return body
	}
	client := newapiintegration.FMGoSeedanceClientID
	if strings.Contains(model, "-fast-") {
		client = newapiintegration.FMGoSeedanceFastClientID
	}
	rewritten, err := sjson.SetBytes(body, "model", client)
	if err != nil {
		return body
	}
	return rewritten
}

func fmgoVideoParamsFromSubmit(c *gin.Context, raw []byte) (string, int, error) {
	resolution, duration, err := fmgoVideoParamsFromBody(raw)
	if err != nil {
		return "", 0, err
	}
	// Doubao BuildRequestBody drops top-level duration aliases. Re-read the
	// original submit body so duration_seconds / seconds still reach the SKU.
	if orig := fmgoOriginalSubmitBody(c); len(orig) > 0 {
		origRes, origDur, origErr := fmgoVideoParamsFromBody(orig)
		if origErr != nil {
			return "", 0, origErr
		}
		if origRes != "" {
			resolution = origRes
		}
		if origDur != 0 {
			duration = origDur
		}
	}
	if c == nil {
		return resolution, duration, nil
	}
	req, reqErr := relaycommon.GetTaskRequest(c)
	if reqErr != nil {
		return resolution, duration, nil
	}
	if res := fmgoResolutionFromTask(req); res != "" {
		resolution = res
	}
	if dur, ok, durErr := fmgoDurationFromTask(req); durErr != nil {
		return "", 0, durErr
	} else if ok {
		duration = dur
	}
	return resolution, duration, nil
}

func fmgoResolutionFromTask(req relaycommon.TaskSubmitReq) string {
	if req.Metadata != nil {
		for _, key := range []string{"resolution", "size"} {
			if value, ok := req.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return strings.TrimSpace(req.Size)
}

func fmgoDurationFromTask(req relaycommon.TaskSubmitReq) (int, bool, error) {
	if req.Duration != 0 {
		return req.Duration, true, nil
	}
	if strings.TrimSpace(req.Seconds) != "" {
		duration, err := newapiintegration.ParseFMGoVideoDuration(req.Seconds)
		return duration, true, err
	}
	if req.Metadata != nil {
		for _, key := range []string{"duration", "seconds", "duration_seconds"} {
			if value, ok := req.Metadata[key]; ok {
				duration, err := parseFMGoDurationValue(value)
				return duration, true, err
			}
		}
	}
	return 0, false, nil
}

func parseFMGoDurationValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("fmgo seedance: duration %v is not an integer", typed)
		}
		return int(typed), nil
	case string:
		return newapiintegration.ParseFMGoVideoDuration(typed)
	default:
		return 0, fmt.Errorf("fmgo seedance: duration has unsupported type %T", value)
	}
}

func fmgoPromptFromSubmit(c *gin.Context, raw []byte) string {
	if c != nil {
		if req, err := relaycommon.GetTaskRequest(c); err == nil {
			if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
				return prompt
			}
		}
	}
	if prompt := firstNonEmptyJSONString(raw, "prompt", "input"); prompt != "" {
		return prompt
	}
	return "probe"
}

func fmgoImagesFromSubmit(c *gin.Context, raw []byte) []string {
	images := make([]string, 0, 4)
	if c != nil {
		if req, err := relaycommon.GetTaskRequest(c); err == nil {
			images = append(images, req.Images...)
			if trimmed := strings.TrimSpace(req.Image); trimmed != "" {
				images = append(images, trimmed)
			}
			if trimmed := strings.TrimSpace(req.InputReference); trimmed != "" {
				images = append(images, trimmed)
			}
		}
	}
	for _, path := range []string{"image", "input_reference"} {
		if value := firstNonEmptyJSONString(raw, path); value != "" {
			images = append(images, value)
		}
	}
	seen := make(map[string]struct{}, len(images))
	out := make([]string, 0, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		out = append(out, image)
	}
	return out
}

func fmgoAspectRatioFromSubmit(c *gin.Context, raw []byte) string {
	if c != nil {
		if req, err := relaycommon.GetTaskRequest(c); err == nil && req.Metadata != nil {
			for _, key := range []string{"aspect_ratio", "aspectRatio", "ratio"} {
				if value, ok := req.Metadata[key].(string); ok {
					return newapiintegration.NormalizeFMGoAspectRatio(value)
				}
			}
		}
	}
	return newapiintegration.NormalizeFMGoAspectRatio(firstNonEmptyJSONString(raw, "aspect_ratio", "aspectRatio", "ratio", "metadata.aspect_ratio", "metadata.aspectRatio", "metadata.ratio"))
}

func fmgoVideoParamsFromBody(raw []byte) (string, int, error) {
	resolution := firstNonEmptyJSONString(raw, "resolution", "size", "metadata.resolution", "metadata.size")
	rawDuration := firstNonEmptyJSONString(raw, "duration", "seconds", "duration_seconds", "metadata.duration", "metadata.seconds", "metadata.duration_seconds")
	if rawDuration == "" {
		for _, path := range []string{"duration", "seconds", "duration_seconds", "metadata.duration", "metadata.seconds", "metadata.duration_seconds"} {
			node := gjson.GetBytes(raw, path)
			if node.Exists() && node.Type == gjson.Number {
				rawDuration = node.Raw
				break
			}
		}
	}
	duration, err := newapiintegration.ParseFMGoVideoDuration(rawDuration)
	if err != nil {
		return "", 0, err
	}
	return resolution, duration, nil
}

func fmgoOriginalSubmitBody(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	stored, ok := c.Get(common.KeyBodyStorage)
	if !ok {
		return nil
	}
	stor, ok := stored.(common.BodyStorage)
	if !ok || stor == nil {
		return nil
	}
	body, err := stor.Bytes()
	if err != nil {
		return nil
	}
	return body
}

func firstNonEmptyJSONString(raw []byte, paths ...string) string {
	for _, path := range paths {
		value := strings.TrimSpace(gjson.GetBytes(raw, path).String())
		if value != "" {
			return value
		}
	}
	return ""
}
