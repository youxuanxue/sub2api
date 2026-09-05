package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

const SettingKeyTKOpenRouterProviderConfig = "tk_openrouter_provider_config"

// OpenRouterProviderSellerKeyName is an optional ops bootstrap label for the
// billing user's OR seller key. Runtime auth does NOT match on name — any API
// key owned by billing_user_id is allowed for the full seller surface.
const OpenRouterProviderSellerKeyName = "openrouter"

// OpenRouterProviderConfig configures the TokenKey seller surface for OpenRouter.
// Supply groups are NOT stored here: BuildOpenRouterProviderCatalog reads
// billing_user_id's user_allowed_groups at runtime (single source of truth).
// Seller auth is billing_user_id ownership (any of that user's API keys).
// AllowedAPIKeyIDs / MonitorAPIKeyIDs remain optional legacy allowlists and both
// grant the full seller surface (catalog + inference) during migration.
// Loop prevention uses scheme C (no aggregator channels on public groups).
// Legacy JSON field "group_ids" is ignored when billing_user_id is set.
type OpenRouterProviderConfig struct {
	Enabled       bool   `json:"enabled"`
	ModelIDPrefix string `json:"model_id_prefix"`
	Slug          string `json:"slug"`
	// GroupIDs is legacy-only. Prefer billing user allowed groups; ignored when BillingUserID > 0.
	GroupIDs               []int64          `json:"group_ids,omitempty"`
	BillingUserID          int64            `json:"billing_user_id"`
	AllowedAPIKeyIDs       []int64          `json:"allowed_api_key_ids,omitempty"`
	MonitorAPIKeyIDs       []int64          `json:"monitor_api_key_ids,omitempty"`
	DefaultContextLen      int              `json:"default_context_length"`
	CapacityTPM            *int64           `json:"capacity_tpm"`
	ModelCapacityTPM       map[string]int64 `json:"model_capacity_tpm"`
	DatacenterCountryCodes []string         `json:"datacenter_country_codes"`
	// CatalogExcludedModelIDs omits internal model ids from GET /openrouter/v1/models.
	// Owner: tk_openrouter_provider_config JSON (see ops/pricing/examples/openrouter-provider-config.example.json).
	CatalogExcludedModelIDs []string `json:"catalog_excluded_model_ids,omitempty"`
	// StreamOnlyModelIDs marks chat models that require stream=true on /v1/chat/completions.
	StreamOnlyModelIDs []string `json:"stream_only_model_ids,omitempty"`

	// P2 onboarding URLs surfaced to ops / provider application forms.
	PrivacyPolicyURL      string `json:"privacy_policy_url"`
	TermsOfServiceURL     string `json:"terms_of_service_url"`
	StatusPageURL         string `json:"status_page_url"`
	InvoicingContactEmail string `json:"invoicing_contact_email"`
	ProviderDisplayName   string `json:"provider_display_name"`
}

func DefaultOpenRouterProviderConfig() OpenRouterProviderConfig {
	return OpenRouterProviderConfig{
		ModelIDPrefix:          "tokenkey/",
		Slug:                   "tokenkey",
		DefaultContextLen:      200000,
		DatacenterCountryCodes: []string{"US"},
		PrivacyPolicyURL:       "https://tokenkey.dev/privacy",
		TermsOfServiceURL:      "https://tokenkey.dev/terms",
		ProviderDisplayName:    "TokenKey",
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
	if len(cfg.DatacenterCountryCodes) == 0 {
		cfg.DatacenterCountryCodes = DefaultOpenRouterProviderConfig().DatacenterCountryCodes
	}
	if strings.TrimSpace(cfg.PrivacyPolicyURL) == "" {
		cfg.PrivacyPolicyURL = DefaultOpenRouterProviderConfig().PrivacyPolicyURL
	}
	if strings.TrimSpace(cfg.TermsOfServiceURL) == "" {
		cfg.TermsOfServiceURL = DefaultOpenRouterProviderConfig().TermsOfServiceURL
	}
	if strings.TrimSpace(cfg.ProviderDisplayName) == "" {
		cfg.ProviderDisplayName = DefaultOpenRouterProviderConfig().ProviderDisplayName
	}
	cfg.CatalogExcludedModelIDs = normalizeOpenRouterProviderModelIDs(cfg.CatalogExcludedModelIDs)
	cfg.StreamOnlyModelIDs = normalizeOpenRouterProviderModelIDs(cfg.StreamOnlyModelIDs)
	return cfg, nil
}

func normalizeOpenRouterProviderModelIDs(ids []string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (c OpenRouterProviderConfig) CatalogExcluded(sourceID string) bool {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return false
	}
	for _, id := range c.CatalogExcludedModelIDs {
		if strings.TrimSpace(id) == sourceID {
			return true
		}
	}
	return false
}

func (c OpenRouterProviderConfig) StreamOnly(sourceID string) bool {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return false
	}
	for _, id := range c.StreamOnlyModelIDs {
		if strings.TrimSpace(id) == sourceID {
			return true
		}
	}
	return false
}

func openRouterProviderEnrichCatalogItem(item *OpenRouterProviderModel, cfg OpenRouterProviderConfig, sourceID string) {
	if item == nil || !cfg.StreamOnly(sourceID) {
		return
	}
	if item.OpenRouter == nil {
		item.OpenRouter = map[string]string{}
	}
	item.OpenRouter["stream_required"] = "true"
	const suffix = " Chat completions require stream=true."
	if !strings.Contains(item.Description, "stream=true") {
		item.Description += suffix
	}
}

func (c OpenRouterProviderConfig) isBillingUser(userID int64) bool {
	return c.BillingUserID > 0 && c.BillingUserID == userID
}

// AllowsSellerAPIKey is the single seller-surface allow check: billing-user
// ownership, or a legacy numeric id allowlist entry.
func (c OpenRouterProviderConfig) AllowsSellerAPIKey(apiKeyID, userID int64) bool {
	if !c.Enabled || apiKeyID <= 0 {
		return false
	}
	if c.isBillingUser(userID) {
		return true
	}
	for _, id := range c.AllowedAPIKeyIDs {
		if id == apiKeyID {
			return true
		}
	}
	for _, id := range c.MonitorAPIKeyIDs {
		if id == apiKeyID {
			return true
		}
	}
	return false
}

// AllowsInferenceAPIKey allows catalog + inference (same seller surface).
func (c OpenRouterProviderConfig) AllowsInferenceAPIKey(apiKeyID, userID int64) bool {
	return c.AllowsSellerAPIKey(apiKeyID, userID)
}

func (c OpenRouterProviderConfig) CanAccessCatalog(apiKeyID, userID int64) bool {
	return c.AllowsSellerAPIKey(apiKeyID, userID)
}

// AllowsAPIKey keeps the previous name for catalog/inference allow checks used in tests.
func (c OpenRouterProviderConfig) AllowsAPIKey(apiKeyID, userID int64) bool {
	return c.AllowsSellerAPIKey(apiKeyID, userID)
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

func (c OpenRouterProviderConfig) InternalModelID(publicID string) (string, bool) {
	publicID = strings.TrimSpace(publicID)
	prefix := strings.TrimSpace(c.ModelIDPrefix)
	if prefix == "" {
		prefix = "tokenkey/"
	}
	if !strings.HasPrefix(publicID, prefix) {
		return "", false
	}
	internal := strings.TrimSpace(strings.TrimPrefix(publicID, prefix))
	if internal == "" {
		return "", false
	}
	return internal, true
}

func (c OpenRouterProviderConfig) CatalogBaseURL(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = "https://api.tokenkey.dev"
	}
	return base + "/openrouter/v1/models"
}

func (c OpenRouterProviderConfig) InferenceBaseURL(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = "https://api.tokenkey.dev"
	}
	return base + "/v1/chat/completions"
}

func (c OpenRouterProviderConfig) ImagesBaseURL(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = "https://api.tokenkey.dev"
	}
	return base + "/openrouter/v1/images"
}

func (c OpenRouterProviderConfig) VideosBaseURL(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = "https://api.tokenkey.dev"
	}
	return base + "/openrouter/v1/videos"
}
