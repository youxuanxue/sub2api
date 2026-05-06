package newapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/ollama"
)

// UpstreamModelFetchAllowed matches new-api web MODEL_FETCHABLE_CHANNEL_TYPES (channel.constants.js).
func UpstreamModelFetchAllowed(channelType int) bool {
	switch channelType {
	case 1, 4, 14, 34, 17, 26, 27, 24, 47, 25, 20, 23, 31, 40, 42, 48, 43, 45, 54:
		return true
	default:
		return false
	}
}

// IsKnownChannelType reports whether t is a valid New API channel type id (excluding unknown/dummy).
func IsKnownChannelType(t int) bool {
	return t > 0 && t < newapiconstant.ChannelTypeDummy
}

// FetchUpstreamModelList mirrors new-api controller.FetchModels for admin "获取模型列表"
// (OpenRouter and other OpenAI-compatible bases use GET /v1/models).
//
// Returns []rawDiscoveredModel — each entry carries the upstream id plus a
// ProviderUnavailable flag set when the provider metadata explicitly marks the
// model as deprecated/disabled/embedding-only. Caller (typically
// DiscoveryFilter.Apply) is responsible for the model_availability table check
// + pricing_status tagging.
//
// Per docs/approved/pricing-availability-source-of-truth.md §2.4 (Goal 1).
func FetchUpstreamModelList(ctx context.Context, baseURL string, channelType int, apiKey string) ([]rawDiscoveredModel, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("api key is required")
	}
	if i := strings.IndexByte(key, '\n'); i >= 0 {
		key = key[:i]
	}

	base := strings.TrimSpace(baseURL)
	if base == "" {
		if channelType <= 0 || channelType >= len(newapiconstant.ChannelBaseURLs) {
			return nil, fmt.Errorf("base_url is required for channel type %d", channelType)
		}
		base = newapiconstant.ChannelBaseURLs[channelType]
	}
	base = strings.TrimRight(base, "/")
	base = NormalizeArkChannelBaseURL(channelType, base)
	// OpenAI-compat bases are host roots; users sometimes paste .../v1 — avoid /v1/v1/models.
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimRight(strings.TrimSuffix(base, "/v1"), "/")
	}
	if base == "" {
		return nil, fmt.Errorf("base_url is required: no default URL for channel type %d", channelType)
	}

	switch channelType {
	case newapiconstant.ChannelTypeVolcEngine, newapiconstant.ChannelTypeDoubaoVideo:
		return fetchOpenAICompatModels(ctx, base+"/api/v3/models", key)
	case newapiconstant.ChannelTypeOllama:
		// Ollama local API doesn't expose the same metadata; we keep the upstream
		// helper output and let the DiscoveryFilter rely on availability table +
		// pricing intersection only.
		models, err := ollama.FetchOllamaModels(base, key)
		if err != nil {
			return nil, fmt.Errorf("ollama: %w", err)
		}
		out := make([]rawDiscoveredModel, 0, len(models))
		for _, m := range models {
			out = append(out, rawDiscoveredModel{ID: m.Name})
		}
		return out, nil
	case newapiconstant.ChannelTypeGemini:
		// TK fetcher (not the upstream FetchGeminiModels) so we retain
		// supportedGenerationMethods and can mark models without
		// generateContent as ProviderUnavailable.
		return fetchGeminiModelsWithMetadata(ctx, base, key)
	default:
		// Moonshot 国内/国际密钥不互通：在保存账号时已解析并固化 base_url（moonshot_resolve_save.go）。
		// 此处若用户仍填官方 cn/ai 根（或空），再并行探测一次，避免管理员「获取模型列表」走错区域；非官方 host 不探测。
		if channelType == newapiconstant.ChannelTypeMoonshot && ShouldResolveMoonshotBaseURLAtSave(base) {
			resolved, err := ResolveMoonshotRegionalBaseAtSave(ctx, key)
			if err != nil {
				return nil, err
			}
			base = resolved
		}
		return fetchOpenAICompatModels(ctx, base+"/v1/models", key)
	}
}

// openAICompatModelEntry mirrors the OpenAI /v1/models response shape with the
// metadata fields that signal "explicitly unavailable". Most OpenAI-compatible
// providers omit these fields, in which case ProviderUnavailable=false (no-op
// at step [1] of DiscoveryFilter.Apply).
type openAICompatModelEntry struct {
	ID         string `json:"id"`
	Deprecated bool   `json:"deprecated"`
	Permission []struct {
		Status string `json:"status"`
	} `json:"permission"`
}

// fetchOpenAICompatModels fetches model IDs from an OpenAI-compatible
// GET /models endpoint AND captures explicit-unavailable signals when the
// provider populates them.
func fetchOpenAICompatModels(ctx context.Context, url, apiKey string) ([]rawDiscoveredModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		s := string(body)
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(s))
	}

	var result struct {
		Data []openAICompatModelEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models json: %w", err)
	}
	out := make([]rawDiscoveredModel, 0, len(result.Data))
	for _, m := range result.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		entry := rawDiscoveredModel{ID: id}
		// Provider-marked deprecated → unavailable.
		if m.Deprecated {
			entry.ProviderUnavailable = true
		}
		// Permission[].status="deprecated"/"disabled"/"retired" → unavailable.
		// (OpenAI's documented schema; most compat providers leave this empty.)
		for _, p := range m.Permission {
			s := strings.ToLower(strings.TrimSpace(p.Status))
			if s == "deprecated" || s == "disabled" || s == "retired" {
				entry.ProviderUnavailable = true
				break
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// geminiV1BetaModelsResponse mirrors the subset of fields we need from
// Gemini's /v1beta/models. Avoids importing the upstream relay/channel/gemini
// helper because it strips supportedGenerationMethods before returning.
type geminiV1BetaModelsResponse struct {
	Models []struct {
		Name                       string   `json:"name"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} `json:"models"`
	NextPageToken string `json:"nextPageToken"`
}

// fetchGeminiModelsWithMetadata is a TK-only Gemini fetcher that retains
// supportedGenerationMethods so we can mark embedding-only models (no
// generateContent) as ProviderUnavailable. The pagination loop mirrors the
// upstream helper but with a richer DTO; the safety bounds (100 pages, 30s
// per-request timeout) are kept.
func fetchGeminiModelsWithMetadata(ctx context.Context, baseURL, apiKey string) ([]rawDiscoveredModel, error) {
	const maxPages = 100
	out := make([]rawDiscoveredModel, 0, 64)
	nextPageToken := ""

	for page := 0; page < maxPages; page++ {
		url := baseURL + "/v1beta/models"
		if nextPageToken != "" {
			url += "?pageToken=" + nextPageToken
		}

		pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(pageCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("gemini: new request: %w", err)
		}
		req.Header.Set("x-goog-api-key", apiKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("gemini: request failed: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("gemini: upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var pageResp geminiV1BetaModelsResponse
		if err := json.NewDecoder(resp.Body).Decode(&pageResp); err != nil {
			_ = resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("gemini: decode: %w", err)
		}
		_ = resp.Body.Close()
		cancel()

		for _, m := range pageResp.Models {
			id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
			if id == "" {
				continue
			}
			entry := rawDiscoveredModel{ID: id}
			// supportedGenerationMethods missing generateContent →
			// embedding-only / unavailable for the use case TokenKey routes.
			if !geminiSupportsGenerateContent(m.SupportedGenerationMethods) {
				entry.ProviderUnavailable = true
			}
			out = append(out, entry)
		}

		if pageResp.NextPageToken == "" {
			break
		}
		nextPageToken = pageResp.NextPageToken
	}
	return out, nil
}

// geminiSupportsGenerateContent returns true when the model exposes the
// generateContent method (or when the field is absent — defensive against
// schema drift). Embedding-only models that only list "embedContent" return
// false.
func geminiSupportsGenerateContent(methods []string) bool {
	if len(methods) == 0 {
		// Defensive: field absent → don't strip; let availability table decide.
		return true
	}
	for _, m := range methods {
		if strings.EqualFold(strings.TrimSpace(m), "generateContent") {
			return true
		}
	}
	return false
}
