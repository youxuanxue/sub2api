package service

import (
	"strings"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/tidwall/gjson"
)

// VideoSubmitBillingParams are request-derived dimensions for tiered video billing.
type VideoSubmitBillingParams struct {
	Resolution    string
	GenerateAudio *bool
	HasInputImage bool
}

func (p VideoSubmitBillingParams) Options() *VideoBillingOptions {
	if p.GenerateAudio == nil && !p.HasInputImage {
		return nil
	}
	return &VideoBillingOptions{
		GenerateAudio: p.GenerateAudio,
		HasInputImage: p.HasInputImage,
	}
}

// VideoSubmitBillingParamsFromBody extracts billing dimensions from an OpenAI-compat video submit body.
func VideoSubmitBillingParamsFromBody(body []byte) VideoSubmitBillingParams {
	var out VideoSubmitBillingParams
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return out
	}
	for _, path := range []string{"resolution", "size", "metadata.resolution", "metadata.size"} {
		if v := strings.TrimSpace(gjson.GetBytes(body, path).String()); v != "" {
			if normalized, ok := newapiintegration.NormalizeVideoTaskResolution(v); ok {
				out.Resolution = normalized
			} else {
				out.Resolution = v
			}
			break
		}
	}
	if md := gjson.GetBytes(body, "metadata"); md.Type == gjson.String && md.String() != "" {
		parsed := gjson.Parse(md.String())
		if out.Resolution == "" {
			for _, key := range []string{"resolution", "size"} {
				if v := strings.TrimSpace(parsed.Get(key).String()); v != "" {
					out.Resolution = v
					break
				}
			}
		}
		for _, key := range []string{"generateAudio", "generate_audio"} {
			if v, ok := strictGJSONBool(parsed.Get(key)); ok {
				out.GenerateAudio = &v
				break
			}
		}
	}
	// Public top-level fields win over compatibility metadata, matching the
	// bridge normalization that copies them into the adaptor request.
	for _, path := range []string{"generateAudio", "generate_audio", "metadata.generateAudio", "metadata.generate_audio"} {
		if v, ok := strictGJSONBool(gjson.GetBytes(body, path)); ok {
			out.GenerateAudio = &v
			break
		}
	}
	if strings.TrimSpace(gjson.GetBytes(body, "image").String()) != "" {
		out.HasInputImage = true
	}
	for _, parts := range []gjson.Result{
		gjson.GetBytes(body, "content"),
		gjson.GetBytes(body, "metadata.content"),
	} {
		if !parts.IsArray() {
			continue
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			typ := part.Get("type").String()
			if typ == "image_url" || part.Get("image_url").Exists() {
				out.HasInputImage = true
				return false
			}
			return true
		})
		if out.HasInputImage {
			break
		}
	}
	return out
}

func strictGJSONBool(value gjson.Result) (bool, bool) {
	switch value.Type {
	case gjson.True:
		return true, true
	case gjson.False:
		return false, true
	default:
		return false, false
	}
}
