package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// SupplierUpstreamModelEntry is one row from a supplier OpenAI-compatible models list.
type SupplierUpstreamModelEntry struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"`
}

// SupplierUpstreamModelsLister fetches the live model catalog for a supplier endpoint.
type SupplierUpstreamModelsLister interface {
	ListSupplierUpstreamModels(ctx context.Context, endpoint, credential string) ([]SupplierUpstreamModelEntry, error)
}

func buildSupplierModelsListURL(transport supplierManagedTransport) string {
	if transport.ChannelType == newapiconstant.ChannelTypeBaiduV2 {
		return strings.TrimRight(newapiintegration.QianfanBaseURL, "/") + "/v2/models"
	}
	return buildOpenAIModelsURL(transport.Endpoint)
}

func (s *AccountTestService) ListSupplierUpstreamModels(
	ctx context.Context,
	endpoint, credential string,
) ([]SupplierUpstreamModelEntry, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, newUpstreamModelSyncConfigError("Upstream HTTP client is not configured", nil)
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return nil, newUpstreamModelSyncConfigError("No supplier API key is available", nil)
	}
	transport, err := resolveSupplierManagedTransport(endpoint)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid supplier endpoint", err)
	}
	modelsURL := buildSupplierModelsListURL(transport)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid supplier model list URL", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)

	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: transport.ChannelType, Concurrency: 1,
		Credentials: map[string]any{
			"base_url": transport.Endpoint,
			"api_key":  credential,
		},
	}
	resp, err := s.doUpstreamModelsRequest(req, "", account)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to request supplier model list", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, upstreamModelsBodyLimit+1))
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to read supplier model list", err)
	}
	if int64(len(body)) > upstreamModelsBodyLimit {
		return nil, newUpstreamModelSyncUpstreamError(
			"Supplier model list response is too large",
			fmt.Errorf("response exceeds %d bytes", upstreamModelsBodyLimit),
		)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamModelSyncUpstreamError(
			fmt.Sprintf("Supplier model list request failed with HTTP %d", resp.StatusCode),
			fmt.Errorf("supplier model list returned HTTP %d", resp.StatusCode),
		)
	}
	entries, err := extractSupplierUpstreamModelEntries(body)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Supplier model list response was not valid JSON", err)
	}
	if len(entries) == 0 {
		return nil, newUpstreamModelSyncUpstreamError("Supplier returned no supported models", nil)
	}
	return entries, nil
}

func extractSupplierUpstreamModelEntries(body []byte) ([]SupplierUpstreamModelEntry, error) {
	var response struct {
		Data   []supplierUpstreamModelJSON `json:"data"`
		Models []supplierUpstreamModelJSON `json:"models"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		var arrayResponse []supplierUpstreamModelJSON
		if arrayErr := json.Unmarshal(body, &arrayResponse); arrayErr != nil {
			return nil, fmt.Errorf("parse supplier model list: %w", err)
		}
		return dedupeSupplierUpstreamEntries(arrayResponse), nil
	}
	combined := make([]supplierUpstreamModelJSON, 0, len(response.Data)+len(response.Models))
	combined = append(combined, response.Data...)
	combined = append(combined, response.Models...)
	if len(combined) == 0 {
		var arrayResponse []supplierUpstreamModelJSON
		if err := json.Unmarshal(body, &arrayResponse); err == nil {
			combined = arrayResponse
		}
	}
	return dedupeSupplierUpstreamEntries(combined), nil
}

type supplierUpstreamModelJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func dedupeSupplierUpstreamEntries(raw []supplierUpstreamModelJSON) []SupplierUpstreamModelEntry {
	seen := make(map[string]struct{}, len(raw))
	result := make([]SupplierUpstreamModelEntry, 0, len(raw))
	for _, entry := range raw {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.Name)
		}
		id = strings.TrimPrefix(id, "models/")
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, SupplierUpstreamModelEntry{
			ID: id, Type: strings.TrimSpace(strings.ToLower(entry.Type)),
		})
	}
	return result
}

func supplierModelMatchKey(raw string) string {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	prevDash := false
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.':
			_, _ = b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !prevDash && b.Len() > 0 {
				_ = b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func matchSupplierUpstreamModelID(configured string, upstream []SupplierUpstreamModelEntry) (string, bool) {
	configured = strings.TrimSpace(configured)
	if configured == "" || len(upstream) == 0 {
		return "", false
	}
	for _, entry := range upstream {
		if entry.ID == configured {
			return entry.ID, true
		}
	}
	want := supplierModelMatchKey(configured)
	if want == "" {
		return "", false
	}
	for _, entry := range upstream {
		if supplierModelMatchKey(entry.ID) == want {
			return entry.ID, true
		}
	}
	return "", false
}

func supplierUpstreamTypeProbeable(modelType string) bool {
	switch strings.TrimSpace(strings.ToLower(modelType)) {
	case "", "chat", "multimodal", "image2text":
		return true
	default:
		return false
	}
}
