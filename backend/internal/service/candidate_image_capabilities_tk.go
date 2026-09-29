package service

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// ImageGenerationCapability describes a complete, independently admitted family
// of requests. Keep profiles separate: unioning independent options can invent
// combinations that no authorized account can execute. No account identity leaks.
type ImageGenerationCapability struct {
	Endpoint        string   `json:"endpoint"`
	AspectRatios    []string `json:"aspect_ratios"`
	Counts          []int    `json:"counts"`
	InputImage      bool     `json:"input_image"`
	SoftAspectRatio bool     `json:"soft_aspect_ratio"`
}

// Gemini's documented image configuration vocabulary; Web's narrower acceptance
// is owned by protocolrouter and is applied through evaluatePath below.
var geminiImageDiscoveryRatios = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}

func (s *UniversalCapabilityService) candidateImageProfile(ctx context.Context, base *CandidateRequest, account *Account, group *Group) *ImageGenerationCapability {
	gemini := antigravity.IsImageModel(base.model)
	if (!gemini || (base.shape != ShapeOpenAIChat && base.shape != ShapeGemini)) && base.shape != ShapeOpenAIImages {
		return nil
	}
	// Gemini uses chat/native, not the unrelated Images adaptor.
	if gemini && base.shape == ShapeOpenAIImages {
		return nil
	}
	p := &ImageGenerationCapability{Endpoint: base.path, AspectRatios: []string{}, Counts: []int{1}, SoftAspectRatio: IsGPTImageGenerationModel(base.model)}
	accepts := func(ratio string, n int, input bool) bool {
		var body map[string]any
		_ = json.Unmarshal(base.body, &body)
		switch base.shape {
		case ShapeOpenAIChat:
			content := any("Generate an image of a landscape")
			if input {
				content = []any{map[string]any{"type": "text", "text": "Generate an image of a landscape"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9X8AAAAASUVORK5CYII="}}}
			}
			body["messages"] = []any{map[string]any{"role": "user", "content": content}}
			body["stream"] = false
			if ratio != "" {
				body["extra_body"] = map[string]any{"google": map[string]any{"image_config": map[string]any{"aspect_ratio": ratio}}}
			}
		case ShapeGemini:
			parts := []any{map[string]any{"text": "Generate an image of a landscape"}}
			if input {
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9X8AAAAASUVORK5CYII="}})
			}
			body["contents"] = []any{map[string]any{"role": "user", "parts": parts}}
			config := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
			if ratio != "" {
				config["imageConfig"] = map[string]any{"aspectRatio": ratio}
			}
			body["generationConfig"] = config
		default:
			if ratio != "" {
				body["aspect_ratio"] = ratio
			}
			body["n"] = n
		}
		encoded, _ := json.Marshal(body)
		request := &CandidateRequest{resolver: base.resolver, key: base.key, groups: base.groups, shape: base.shape, path: base.path, model: base.model, body: encoded, forcePlatform: base.forcePlatform}
		requestCtx := s.resolver.WithRequest(ctx, request.shape, request.path, request.model, encoded)
		path, err := request.evaluatePathWithPreparation(requestCtx, account, group, candidateDiscoveryPathPreparer(request))
		if err != nil || path == nil {
			return false
		}
		if base.shape == ShapeOpenAIImages {
			parsed := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, N: 1}
			if parseOpenAIImagesJSONRequest(encoded, parsed) != nil {
				return false
			}
			applyOpenAIImagesDefaults(parsed)
			if ValidateOpenAIImagesContract(parsed) != nil || validateOpenAIImagesAspectRatio(parsed) != nil {
				return false
			}
			parsed.RequiredCapability = classifyOpenAIImagesCapability(parsed)
			return !IsOpenAICompatPlatform(account.Platform) || account.SupportsOpenAIImageCapability(parsed.RequiredCapabilityForModel(path.model))
		}
		return true
	}
	if !accepts("", 1, false) {
		return nil
	}
	ratios := []string{}
	if gemini {
		ratios = geminiImageDiscoveryRatios
	} else if p.SoftAspectRatio {
		ratios = openAIImagesAllowedAspectRatioList
	}
	for _, ratio := range ratios {
		if accepts(ratio, 1, false) {
			p.AspectRatios = append(p.AspectRatios, ratio)
		}
	}
	// Prove complete combinations on this path, including the default request.
	variants := append([]string{""}, p.AspectRatios...)
	if gemini {
		p.InputImage = !slices.ContainsFunc(variants, func(r string) bool { return !accepts(r, 1, true) })
	}
	if base.shape == ShapeOpenAIImages {
		for n := 2; n <= 4; n++ {
			if !slices.ContainsFunc(variants, func(r string) bool { return !accepts(r, n, false) }) {
				p.Counts = append(p.Counts, n)
			}
		}
	}
	return p
}

func appendImageProfile(profiles []ImageGenerationCapability, profile *ImageGenerationCapability) []ImageGenerationCapability {
	if profile == nil {
		return profiles
	}
	for _, old := range profiles {
		if old.Endpoint == profile.Endpoint && old.InputImage == profile.InputImage && old.SoftAspectRatio == profile.SoftAspectRatio && slices.Equal(old.AspectRatios, profile.AspectRatios) && slices.Equal(old.Counts, profile.Counts) {
			return profiles
		}
	}
	return append(profiles, *profile)
}
