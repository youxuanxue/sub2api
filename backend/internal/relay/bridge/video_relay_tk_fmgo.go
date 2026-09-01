package bridge

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

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
// FMGo resells Seedance as baked SKUs (feimiao-v2[-fast]-{res}-{dur}s) behind a
// NewAPI-shaped surface. TokenKey clients send official Ark ids plus
// resolution/duration. Identification is channel_type + base_url only — never
// supplier_source_id. Capability set is pinned in newapiintegration.
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
	return fmt.Sprintf("%s/v1/video/generations", a.baseURL), nil
}

func (a *fmgoTaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return newapichannel.DoTaskApiRequest(a, c, info, requestBody)
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
	uri := fmt.Sprintf("%s/v1/videos/%s", base, taskID)
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := newapiservice.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("fmgo video fetch: new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *fmgoTaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	inner, err := a.TaskAdaptor.BuildRequestBody(c, info)
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, fmt.Errorf("fmgo video submit: embedded adaptor produced no body")
	}
	raw, err := io.ReadAll(inner)
	if err != nil {
		return nil, fmt.Errorf("fmgo video submit: read embedded body: %w", err)
	}
	client := fmgoSeedanceClientFromRelay(info, gjson.GetBytes(raw, "model").String())
	resolution, duration, durErr := fmgoVideoParamsFromSubmit(c, raw)
	if durErr != nil {
		return nil, durErr
	}
	upstream, skuErr := newapiintegration.FMGoSeedanceUpstreamSKU(client, resolution, duration)
	if skuErr != nil {
		return nil, skuErr
	}
	if upstream == gjson.GetBytes(raw, "model").String() {
		return bytes.NewReader(raw), nil
	}
	rewritten, err := sjson.SetBytes(raw, "model", upstream)
	if err != nil {
		return nil, fmt.Errorf("fmgo video submit: rewrite model to %q: %w", upstream, err)
	}
	if info != nil {
		info.UpstreamModelName = upstream
	}
	return bytes.NewReader(rewritten), nil
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
		for _, key := range []string{"duration", "seconds"} {
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

func fmgoVideoParamsFromBody(raw []byte) (string, int, error) {
	resolution := firstNonEmptyJSONString(raw, "resolution", "size", "metadata.resolution", "metadata.size")
	rawDuration := firstNonEmptyJSONString(raw, "duration", "seconds", "metadata.duration", "metadata.seconds")
	if rawDuration == "" {
		for _, path := range []string{"duration", "seconds", "metadata.duration", "metadata.seconds"} {
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

func firstNonEmptyJSONString(raw []byte, paths ...string) string {
	for _, path := range paths {
		value := strings.TrimSpace(gjson.GetBytes(raw, path).String())
		if value != "" {
			return value
		}
	}
	return ""
}
