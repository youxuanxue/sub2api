package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	newapichannel "github.com/QuantumNous/new-api/relay/channel"
	taskali "github.com/QuantumNous/new-api/relay/channel/task/ali"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// TokenKey: Ali / DashScope video task adaptor companion.
//
// Upstream new-api task/ali shapes Wan-family requests (img_url / size WxH).
// HappyHorse-1.1 uses the same video-synthesis endpoint and X-DashScope-Async
// header, but a different input dialect:
//
//	t2v: input.prompt + parameters.{resolution,ratio,duration}
//	i2v: input.media[{type:first_frame,url}] (exactly one)
//	r2v: input.media[{type:reference_image,url}] (1–9)
//
// new-api is a pinned upstream we must not patch from this repo (CLAUDE.md §4),
// so HappyHorse body conversion lives here. Wan-family traffic still delegates
// to the embedded adaptor unchanged. TaskContinuation / RequestPlan / public
// /v1/video/generations ownership stay on the shared video_relay path — this
// wrapper only owns the wire dialect for HappyHorse SKUs.
type aliVideoTaskAdaptor struct {
	*taskali.TaskAdaptor
}

func newAliVideoTaskAdaptor() *aliVideoTaskAdaptor {
	return &aliVideoTaskAdaptor{TaskAdaptor: &taskali.TaskAdaptor{}}
}

func (a *aliVideoTaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	// Preserve method dispatch so BuildRequestBody on this wrapper runs for
	// HappyHorse. Promoted TaskAdaptor.DoRequest would bind the inner receiver.
	return newapichannel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *aliVideoTaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	taskReq, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, errors.Wrap(err, "get_task_request_failed")
	}
	upstreamModel := happyHorseUpstreamModel(info, taskReq)
	if !isHappyHorseVideoModel(upstreamModel) {
		return a.TaskAdaptor.BuildRequestBody(c, info)
	}
	aliReq, err := buildHappyHorseAliRequest(upstreamModel, taskReq)
	if err != nil {
		return nil, err
	}
	bodyBytes, err := common.Marshal(aliReq)
	if err != nil {
		return nil, errors.Wrap(err, "marshal_happyhorse_request_failed")
	}
	return bytes.NewReader(bodyBytes), nil
}

func (a *aliVideoTaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	taskReq, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}
	upstreamModel := happyHorseUpstreamModel(info, taskReq)
	if !isHappyHorseVideoModel(upstreamModel) {
		return a.TaskAdaptor.EstimateBilling(c, info)
	}
	duration, err := happyHorseDuration(taskReq)
	if err != nil {
		return nil
	}
	// TokenKey bills via overlay video_price_tiers; seconds is the only
	// adaptor-side ratio the shared path still consumes for duration scaling.
	return map[string]float64{"seconds": float64(duration)}
}

func happyHorseUpstreamModel(info *relaycommon.RelayInfo, req relaycommon.TaskSubmitReq) string {
	if info != nil && info.IsModelMapped && strings.TrimSpace(info.UpstreamModelName) != "" {
		return strings.TrimSpace(info.UpstreamModelName)
	}
	if info != nil && strings.TrimSpace(info.UpstreamModelName) != "" {
		return strings.TrimSpace(info.UpstreamModelName)
	}
	return strings.TrimSpace(req.Model)
}

func isHappyHorseVideoModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "happyhorse-")
}

type happyHorseMedia struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type happyHorseInput struct {
	Prompt string            `json:"prompt,omitempty"`
	Media  []happyHorseMedia `json:"media,omitempty"`
}

type happyHorseParameters struct {
	Resolution string `json:"resolution,omitempty"`
	Ratio      string `json:"ratio,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Watermark  *bool  `json:"watermark,omitempty"`
	Seed       *int   `json:"seed,omitempty"`
}

type happyHorseRequest struct {
	Model      string                `json:"model"`
	Input      happyHorseInput       `json:"input"`
	Parameters *happyHorseParameters `json:"parameters,omitempty"`
}

func buildHappyHorseAliRequest(upstreamModel string, req relaycommon.TaskSubmitReq) (*happyHorseRequest, error) {
	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	prompt := strings.TrimSpace(req.Prompt)
	images := happyHorseImageURLs(req)

	meta := req.Metadata
	if meta == nil {
		meta = map[string]any{}
	}

	duration, err := happyHorseDuration(req)
	if err != nil {
		return nil, err
	}
	resolution, err := happyHorseResolution(req, meta)
	if err != nil {
		return nil, err
	}

	params := &happyHorseParameters{
		Resolution: resolution,
		Duration:   duration,
	}
	if ratio, ok, err := happyHorseRatio(meta); err != nil {
		return nil, err
	} else if ok {
		params.Ratio = ratio
	} else if strings.Contains(model, "-t2v") || strings.Contains(model, "-r2v") {
		params.Ratio = "16:9"
	}
	if watermark, ok, err := happyHorseBoolMeta(meta, "watermark"); err != nil {
		return nil, err
	} else if ok {
		params.Watermark = &watermark
	} else {
		// Match Wan companion default: TokenKey ships clean output unless asked.
		off := false
		params.Watermark = &off
	}
	if seed, ok, err := happyHorseIntMeta(meta, "seed"); err != nil {
		return nil, err
	} else if ok {
		params.Seed = &seed
	}

	out := &happyHorseRequest{
		Model:      upstreamModel,
		Parameters: params,
	}

	switch {
	case strings.Contains(model, "-t2v"):
		if prompt == "" {
			return nil, fmt.Errorf("happyhorse t2v requires prompt")
		}
		if len(images) > 0 {
			return nil, fmt.Errorf("happyhorse t2v does not accept image input")
		}
		out.Input = happyHorseInput{Prompt: prompt}
	case strings.Contains(model, "-i2v"):
		if len(images) != 1 {
			return nil, fmt.Errorf("happyhorse i2v requires exactly one first_frame image, got %d", len(images))
		}
		out.Input = happyHorseInput{
			Prompt: prompt,
			Media:  []happyHorseMedia{{Type: "first_frame", URL: images[0]}},
		}
	case strings.Contains(model, "-r2v"):
		if len(images) < 1 || len(images) > 9 {
			return nil, fmt.Errorf("happyhorse r2v requires 1-9 reference_image inputs, got %d", len(images))
		}
		media := make([]happyHorseMedia, 0, len(images))
		for _, url := range images {
			media = append(media, happyHorseMedia{Type: "reference_image", URL: url})
		}
		out.Input = happyHorseInput{Prompt: prompt, Media: media}
	default:
		return nil, fmt.Errorf("unsupported happyhorse model %q", upstreamModel)
	}
	return out, nil
}

func happyHorseImageURLs(req relaycommon.TaskSubmitReq) []string {
	out := make([]string, 0, len(req.Images)+2)
	seen := map[string]struct{}{}
	appendURL := func(raw string) {
		u := strings.TrimSpace(raw)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	for _, u := range req.Images {
		appendURL(u)
	}
	appendURL(req.InputReference)
	appendURL(req.Image)
	return out
}

func happyHorseDuration(req relaycommon.TaskSubmitReq) (int, error) {
	duration := req.Duration
	if duration <= 0 && strings.TrimSpace(req.Seconds) != "" {
		seconds, err := strconv.Atoi(strings.TrimSpace(req.Seconds))
		if err != nil {
			return 0, errors.Wrap(err, "happyhorse duration seconds")
		}
		duration = seconds
	}
	if duration <= 0 {
		duration = 5
	}
	if duration < 3 || duration > 15 {
		return 0, fmt.Errorf("happyhorse duration must be in [3,15], got %d", duration)
	}
	return duration, nil
}

func happyHorseResolution(req relaycommon.TaskSubmitReq, meta map[string]any) (string, error) {
	raw := strings.TrimSpace(req.Size)
	if raw == "" {
		if v, ok := meta["resolution"].(string); ok {
			raw = strings.TrimSpace(v)
		}
	}
	if raw == "" {
		if v, ok := meta["size"].(string); ok {
			raw = strings.TrimSpace(v)
		}
	}
	if raw == "" {
		return "1080P", nil
	}
	normalized, ok := newapiintegration.NormalizeVideoTaskResolution(raw)
	if !ok {
		return "", fmt.Errorf("happyhorse resolution %q is unsupported", raw)
	}
	switch normalized {
	case newapiintegration.VideoTaskResolution480P,
		newapiintegration.VideoTaskResolution720P,
		newapiintegration.VideoTaskResolution1080P:
		return strings.ToUpper(normalized), nil
	default:
		return "", fmt.Errorf("happyhorse resolution %q is unsupported", raw)
	}
}

func happyHorseRatio(meta map[string]any) (string, bool, error) {
	raw, ok := meta["ratio"].(string)
	if !ok {
		return "", false, nil
	}
	ratio := strings.TrimSpace(raw)
	if ratio == "" {
		return "", false, fmt.Errorf("happyhorse metadata.ratio must not be empty")
	}
	switch ratio {
	case "16:9", "9:16", "1:1", "4:3", "3:4", "4:5", "5:4", "9:21", "21:9":
		return ratio, true, nil
	default:
		return "", false, fmt.Errorf("happyhorse ratio %q is unsupported", ratio)
	}
}

func happyHorseBoolMeta(meta map[string]any, key string) (bool, bool, error) {
	v, ok := meta[key]
	if !ok {
		return false, false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, false, fmt.Errorf("happyhorse metadata.%s must be a boolean", key)
	}
	return b, true, nil
}

func happyHorseIntMeta(meta map[string]any, key string) (int, bool, error) {
	v, ok := meta[key]
	if !ok {
		return 0, false, nil
	}
	switch n := v.(type) {
	case float64:
		return int(n), true, nil
	case int:
		return n, true, nil
	case int64:
		return int(n), true, nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("happyhorse metadata.%s must be an integer", key)
		}
		return int(i), true, nil
	default:
		return 0, false, fmt.Errorf("happyhorse metadata.%s must be an integer", key)
	}
}
