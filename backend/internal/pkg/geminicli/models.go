package geminicli

// Model represents a selectable Gemini model for UI/testing purposes.
// Keep JSON fields consistent with existing frontend expectations.
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels is the curated Gemini model list used by the admin UI "test account" flow.
var DefaultModels = []Model{
	{ID: "gemini-2.5-flash", Type: "model", DisplayName: "Gemini 2.5 Flash", CreatedAt: ""},
	{ID: "gemini-2.5-flash-lite", Type: "model", DisplayName: "Gemini 2.5 Flash Lite", CreatedAt: ""},
	{ID: "gemini-2.5-pro", Type: "model", DisplayName: "Gemini 2.5 Pro", CreatedAt: ""},
	{ID: "gemini-3.5-flash-lite", Type: "model", DisplayName: "Gemini 3.5 Flash Lite", CreatedAt: ""},
	{ID: "gemini-3.6-flash", Type: "model", DisplayName: "Gemini 3.6 Flash", CreatedAt: ""},
	{ID: "imagen-4.0-fast-generate-001", Type: "model", DisplayName: "Imagen 4.0 Fast", CreatedAt: ""},
	{ID: "imagen-4.0-generate-001", Type: "model", DisplayName: "Imagen 4.0", CreatedAt: ""},
	{ID: "imagen-4.0-ultra-generate-001", Type: "model", DisplayName: "Imagen 4.0 Ultra", CreatedAt: ""},
	{ID: "veo-3.1-generate-001", Type: "model", DisplayName: "Veo 3.1", CreatedAt: ""},
}

// GoogleOneModels is the conservative model set exposed for legacy Google One
// OAuth accounts. Newer subscription models are served through Antigravity
// OAuth rather than the retired consumer Gemini CLI / Code Assist channel.
var GoogleOneModels = []Model{
	{ID: "gemini-2.5-flash", Type: "model", DisplayName: "Gemini 2.5 Flash", CreatedAt: ""},
	{ID: "gemini-2.5-pro", Type: "model", DisplayName: "Gemini 2.5 Pro", CreatedAt: ""},
	{ID: "gemini-2.0-flash", Type: "model", DisplayName: "Gemini 2.0 Flash", CreatedAt: ""},
}

// GoogleOneModelMapping returns a new whitelist map for each account so callers
// cannot mutate the package-level catalog.
func GoogleOneModelMapping() map[string]string {
	mapping := make(map[string]string, len(GoogleOneModels))
	for _, model := range GoogleOneModels {
		mapping[model.ID] = model.ID
	}
	return mapping
}

// DefaultTestModel is the default model to preselect in test flows.
const DefaultTestModel = "gemini-2.5-flash"

// ModelsForIDs synthesizes a []Model for the given (servable) ids, preferring the
// canonical DefaultModels entry when present and synthesizing otherwise. Shared by
// the gateway /v1/models fallback and the admin available-models surface so the
// two never drift on the synthesized display metadata.
func ModelsForIDs(ids []string) []Model {
	byID := make(map[string]Model, len(DefaultModels))
	for _, m := range DefaultModels {
		byID[m.ID] = m
	}
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, Model{ID: id, Type: "model", DisplayName: id})
	}
	return out
}
