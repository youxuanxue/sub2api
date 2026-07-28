package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

const SettingKeyTKOpenRouterProviderConfig = "tk_openrouter_provider_config"

// OpenRouterProviderConfig configures the TokenKey seller surface for OpenRouter.
// OR dedicated groups do not need is_exclusive=true; loop prevention uses scheme C
// (no aggregator channels on public groups) plus explicit group_ids here.
type OpenRouterProviderConfig struct {
	Enabled           bool    `json:"enabled"`
	ModelIDPrefix     string  `json:"model_id_prefix"`
	Slug              string  `json:"slug"`
	GroupIDs          []int64 `json:"group_ids"`
	BillingUserID     int64   `json:"billing_user_id"`
	AllowedAPIKeyIDs  []int64 `json:"allowed_api_key_ids"`
	DefaultContextLen int     `json:"default_context_length"`
	CapacityTPM       *int64  `json:"capacity_tpm"`
}

func DefaultOpenRouterProviderConfig() OpenRouterProviderConfig {
	return OpenRouterProviderConfig{
		ModelIDPrefix:     "tokenkey/",
		Slug:              "tokenkey",
		DefaultContextLen: 200000,
	}
}

func ParseOpenRouterProviderConfig(raw string) (OpenRouterProviderConfig, error) {
	cfg := DefaultOpenRouterProviderConfig()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return OpenRouterProviderConfig{}, fmt.Errorf("parse tk_openrouter_provider_config: %w", err)
	}
	if strings.TrimSpace(cfg.ModelIDPrefix) == "" {
		cfg.ModelIDPrefix = DefaultOpenRouterProviderConfig().ModelIDPrefix
	}
	if strings.TrimSpace(cfg.Slug) == "" {
		cfg.Slug = DefaultOpenRouterProviderConfig().Slug
	}
	if cfg.DefaultContextLen <= 0 {
		cfg.DefaultContextLen = DefaultOpenRouterProviderConfig().DefaultContextLen
	}
	return cfg, nil
}

func (c OpenRouterProviderConfig) AllowsAPIKey(apiKeyID, userID int64) bool {
	if !c.Enabled {
		return false
	}
	if len(c.AllowedAPIKeyIDs) > 0 {
		for _, id := range c.AllowedAPIKeyIDs {
			if id == apiKeyID {
				return true
			}
		}
		return false
	}
	if c.BillingUserID > 0 && c.BillingUserID == userID {
		return true
	}
	return false
}

func (c OpenRouterProviderConfig) PublicModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	prefix := strings.TrimSpace(c.ModelIDPrefix)
	if prefix == "" {
		prefix = "tokenkey/"
	}
	if strings.HasPrefix(model, prefix) {
		return model
	}
	if strings.Contains(model, "/") {
		return prefix + strings.TrimPrefix(model, "/")
	}
	return prefix + model
}
