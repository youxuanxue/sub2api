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
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/ollama"
)

// UpstreamModelFetchAllowed matches new-api web MODEL_FETCHABLE_CHANNEL_TYPES (channel.constants.js).
func UpstreamModelFetchAllowed(channelType int) bool {
	switch channelType {
	case 1, 4, 14, 34, 17, 26, 27, 24, 47, 25, 20, 23, 31, 40, 42, 48, 43, 45:
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
func FetchUpstreamModelList(ctx context.Context, baseURL string, channelType int, apiKey string) ([]string, error) {
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
	// OpenAI-compat bases are host roots; users sometimes paste .../v1 — avoid /v1/v1/models.
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimRight(strings.TrimSuffix(base, "/v1"), "/")
	}
	if base == "" {
		return nil, fmt.Errorf("base_url is required: no default URL for channel type %d", channelType)
	}

	switch channelType {
	case newapiconstant.ChannelTypeVolcEngine:
		return fetchOpenAICompatModels(ctx, base+"/api/v3/models", key)
	case newapiconstant.ChannelTypeOllama:
		models, err := ollama.FetchOllamaModels(base, key)
		if err != nil {
			return nil, fmt.Errorf("ollama: %w", err)
		}
		names := make([]string, 0, len(models))
		for _, m := range models {
			names = append(names, m.Name)
		}
		return names, nil
	case newapiconstant.ChannelTypeGemini:
		models, err := gemini.FetchGeminiModels(base, key, "")
		if err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
		return models, nil
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

// fetchOpenAICompatModels fetches model IDs from an OpenAI-compatible GET /models endpoint.
func fetchOpenAICompatModels(ctx context.Context, url, apiKey string) ([]string, error) {
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
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models json: %w", err)
	}
	out := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}
