package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	SeedanceEndpointCreate           GrokMediaEndpoint        = "seedance_create"
	SeedanceEndpointStatus           GrokMediaEndpoint        = "seedance_status"
	SeedanceEndpointDelete           GrokMediaEndpoint        = "seedance_delete"
	OpenAIEndpointCapabilitySeedance OpenAIEndpointCapability = "seedance"
)

func (e GrokMediaEndpoint) IsSeedance() bool {
	return e == SeedanceEndpointCreate || e == SeedanceEndpointStatus || e == SeedanceEndpointDelete
}

// SeedanceGroupPlatformAllowed is the direct-key passthrough gate for the
// OpenAI seedance-capability forwarder. newapi video accounts are not in
// that set; they are chosen by video account selection.
func SeedanceGroupPlatformAllowed(platform string) bool {
	switch platform {
	case PlatformOpenAI, PlatformComposite:
		return true
	default:
		return false
	}
}

// SeedanceContentsUsesVideoSelection reports that this contents call must
// select a video-capable account instead of an OpenAI seedance-capability
// account. A candidate request means admission already ran across the key's
// authorized groups; the billing group is filled in only after an account is
// chosen. A direct newapi key is already scoped to that video pool.
func SeedanceContentsUsesVideoSelection(ctx context.Context, groupPlatform string) bool {
	if CandidateRequestFromContext(ctx) != nil {
		return true
	}
	return groupPlatform == PlatformNewAPI
}

// SeedanceContentsAsVideoSubmitBody lets an Ark contents create ride the
// newapi video pipeline. Text parts become prompt. Image parts are copied to
// images and metadata.content so the doubao/XRToken adaptor keeps roles and
// FMGo still sees reference images. Duration is copied to seconds and
// metadata.duration because the doubao adaptor ignores a top-level duration.
// A body that already has prompt and no content array is left unchanged.
func SeedanceContentsAsVideoSubmitBody(body []byte) ([]byte, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	out := body
	if strings.TrimSpace(gjson.GetBytes(body, "prompt").String()) == "" {
		info, err := ParseSeedanceRequest(body)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(info.Prompt) == "" {
			return nil, fmt.Errorf("content must include text")
		}
		out, err = sjson.SetBytes(out, "prompt", info.Prompt)
		if err != nil {
			return nil, err
		}
	}
	content := gjson.GetBytes(out, "content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return out, nil
	}
	return liftSeedanceContentForVideoSubmit(out, content)
}

// liftSeedanceContentForVideoSubmit copies Ark content fields onto the
// carriers the newapi video adaptors already read. Top-level content, duration,
// and ratio stay in place for billing and for adaptors that re-read the
// original body.
func liftSeedanceContentForVideoSubmit(body []byte, content gjson.Result) ([]byte, error) {
	out := body
	var err error
	if !gjson.GetBytes(out, "images").Exists() {
		var images []string
		for _, item := range content.Array() {
			if item.Get("type").String() != "image_url" {
				continue
			}
			if url := strings.TrimSpace(item.Get("image_url.url").String()); url != "" {
				images = append(images, url)
			}
		}
		if len(images) > 0 {
			out, err = sjson.SetBytes(out, "images", images)
			if err != nil {
				return nil, err
			}
		}
	}
	if !gjson.GetBytes(out, "metadata.content").Exists() {
		out, err = sjson.SetRawBytes(out, "metadata.content", []byte(content.Raw))
		if err != nil {
			return nil, err
		}
	}
	duration := gjson.GetBytes(out, "duration")
	if duration.Exists() && (duration.Type == gjson.Number || duration.Type == gjson.String) {
		if !gjson.GetBytes(out, "seconds").Exists() {
			seconds := strings.TrimSpace(duration.String())
			if seconds != "" {
				out, err = sjson.SetBytes(out, "seconds", seconds)
				if err != nil {
					return nil, err
				}
			}
		}
		if !gjson.GetBytes(out, "metadata.duration").Exists() {
			out, err = sjson.SetRawBytes(out, "metadata.duration", []byte(duration.Raw))
			if err != nil {
				return nil, err
			}
		}
	}
	ratio := gjson.GetBytes(out, "ratio")
	if ratio.Type == gjson.String && strings.TrimSpace(ratio.String()) != "" && !gjson.GetBytes(out, "metadata.ratio").Exists() {
		out, err = sjson.SetRawBytes(out, "metadata.ratio", []byte(ratio.Raw))
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SeedanceTaskKey isolates ownership and billing keys from other video providers.
func SeedanceTaskKey(id string) string { return "seedance:" + strings.TrimSpace(id) }

func ParseSeedanceRequest(body []byte) (GrokMediaRequestInfo, error) {
	var info GrokMediaRequestInfo
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return info, fmt.Errorf("request body must be a JSON object")
	}
	model := gjson.GetBytes(body, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return info, fmt.Errorf("model is required")
	}
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return info, fmt.Errorf("content must be a non-empty array")
	}
	info.Model = strings.TrimSpace(model.String())
	var texts []string
	for _, item := range content.Array() {
		switch item.Get("type").String() {
		case "text":
			texts = append(texts, item.Get("text").String())
		case "image_url":
			info.InputImageURLs = append(info.InputImageURLs, item.Get("image_url.url").String())
		}
	}
	info.Prompt = strings.Join(texts, "\n")
	return info, nil
}

func buildSeedanceURL(base string, endpoint GrokMediaEndpoint, taskID string) (string, error) {
	base = strings.TrimRight(base, "/")
	// Accept an origin, a proxy prefix, or the full Ark API base.
	if !strings.HasSuffix(base, "/api/v3") && !strings.HasSuffix(base, "/v3") {
		base += "/api/v3"
	}
	base += "/contents/generations/tasks"
	if endpoint != SeedanceEndpointCreate {
		if err := validateUpstreamPathSegment("Seedance task ID", taskID); err != nil || strings.TrimSpace(taskID) == "" {
			return "", fmt.Errorf("invalid Seedance task ID")
		}
		base += "/" + taskID
	}
	return base, nil
}

// ForwardSeedance preserves the Ark protocol, including multimodal content and
// future fields. Only model is rewritten using the account's configured mapping.
func (s *OpenAIGatewayService) ForwardSeedance(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID string, body []byte) (*OpenAIForwardResult, error) {
	if !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance) || !endpoint.IsSeedance() {
		return nil, fmt.Errorf("seedance requires an OpenAI API key account with a custom base URL")
	}
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	target, err := buildSeedanceURL(base, endpoint, strings.TrimPrefix(taskID, "seedance:"))
	if err != nil {
		return nil, err
	}
	model, upstreamModel := "", ""
	method := http.MethodGet
	switch endpoint {
	case SeedanceEndpointCreate:
		info, parseErr := ParseSeedanceRequest(body)
		if parseErr != nil {
			return nil, parseErr
		}
		model = info.Model
		upstreamModel = account.GetMappedModel(model)
		body, err = sjson.SetBytes(body, "model", upstreamModel)
		if err != nil {
			return nil, err
		}
		method = http.MethodPost
	case SeedanceEndpointDelete:
		method = http.MethodDelete
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, fmt.Errorf("seedance account missing api_key")
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	// Do not retry ambiguous asynchronous creates: the upstream may already have
	// accepted a billable job. Preserve native error codes and response bodies.
	if resp.StatusCode >= 300 {
		writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
		return nil, fmt.Errorf("seedance upstream status %d", resp.StatusCode)
	}
	result := &OpenAIForwardResult{Model: model, BillingModel: model, UpstreamModel: upstreamModel, Duration: time.Since(started), ResponseHeaders: resp.Header.Clone()}
	if endpoint == SeedanceEndpointCreate {
		id := strings.TrimSpace(gjson.GetBytes(responseBody, "id").String())
		if id == "" {
			return nil, fmt.Errorf("seedance create response missing task ID")
		}
		result.ResponseID = SeedanceTaskKey(id)
	}
	if endpoint == SeedanceEndpointStatus {
		result.ResponseID = taskID
		result.UpstreamModel = gjson.GetBytes(responseBody, "model").String()
		if gjson.GetBytes(responseBody, "status").String() == "succeeded" {
			result.Usage.OutputTokens = max(0, int(gjson.GetBytes(responseBody, "usage.completion_tokens").Int()))
		}
	}
	writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
	return result, nil
}
