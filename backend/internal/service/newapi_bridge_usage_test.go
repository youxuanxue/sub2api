package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewAPIBridgeChannelInput_WiresForwardingCredentials pins the contract
// between admin-stored credentials and the new-api relay context for the
// fifth platform's three forwarding-affecting credentials added in US-019:
//
//   - model_mapping  → Gin key "model_mapping"        (read by every relay handler)
//   - openai_organization → Gin key "channel_organization"
//   - status_code_mapping → Gin key "status_code_mapping"
//
// Before US-019, admins could enter model_mapping via the API but no UI
// surface collected it; openai_organization and status_code_mapping had no
// path at all. Forgetting to forward any of them would silently regress
// upstream-compatible behavior (status remap, OpenAI org scoping, model
// alias rewrite) without breaking any test, hence this spec.
func TestNewAPIBridgeChannelInput_WiresForwardingCredentials(t *testing.T) {
	mapping := map[string]any{
		"gpt-4": "gpt-4-turbo",
	}
	account := &Account{
		ID:          7,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 25,
		Credentials: map[string]any{
			"base_url":            "https://api.moonshot.ai",
			"api_key":             "sk-test",
			"model_mapping":       mapping,
			"openai_organization": "  org-abc  ",
			"status_code_mapping": `{"404":"500"}`,
		},
	}

	in := newAPIBridgeChannelInput(account, 42, "moonshot-default")

	if in.ChannelType != 25 {
		t.Fatalf("ChannelType: want 25, got %d", in.ChannelType)
	}
	if in.ChannelID != 7 {
		t.Fatalf("ChannelID: want 7, got %d", in.ChannelID)
	}
	if in.BaseURL != "https://api.moonshot.ai" {
		t.Fatalf("BaseURL: want moonshot.ai, got %q", in.BaseURL)
	}
	if in.APIKey != "sk-test" {
		t.Fatalf("APIKey: want sk-test, got %q", in.APIKey)
	}
	if in.Organization != "org-abc" {
		t.Fatalf("Organization must be trimmed: want %q, got %q", "org-abc", in.Organization)
	}
	if in.StatusCodeMappingJSON != `{"404":"500"}` {
		t.Fatalf("StatusCodeMappingJSON: want %q, got %q", `{"404":"500"}`, in.StatusCodeMappingJSON)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(in.ModelMappingJSON), &got); err != nil {
		t.Fatalf("ModelMappingJSON not valid JSON: %v", err)
	}
	if got["gpt-4"] != "gpt-4-turbo" {
		t.Fatalf("ModelMappingJSON missing gpt-4 entry: %v", got)
	}
}

// TestNewAPIBridgeChannelInput_OmitsEmptyForwardingCredentials guards that
// the bridge does NOT emit non-empty values when admins haven't configured
// the optional fields. PopulateContextKeys only writes Gin keys when fields
// are non-empty, so leaking "{}" or whitespace would silently shadow the
// upstream-default behavior with a no-op mapping.
func TestNewAPIBridgeChannelInput_OmitsEmptyForwardingCredentials(t *testing.T) {
	account := &Account{
		ID:          1,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 36,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
			"api_key":  "sk-test",
			// no model_mapping / openai_organization / status_code_mapping
		},
	}

	in := newAPIBridgeChannelInput(account, 1, "deepseek-default")

	if in.ModelMappingJSON != "" {
		t.Fatalf("ModelMappingJSON should be empty when no mapping configured, got %q", in.ModelMappingJSON)
	}
	if in.Organization != "" {
		t.Fatalf("Organization should be empty when not configured, got %q", in.Organization)
	}
	if in.StatusCodeMappingJSON != "" {
		t.Fatalf("StatusCodeMappingJSON should be empty when not configured, got %q", in.StatusCodeMappingJSON)
	}
}

// TestNewAPIBridgeChannelInput_VertexServiceAccount pins #0: a channel_type 41
// (VertexAI) account with a service-account JSON forwards the SA JSON as the channel
// key, signals VertexKeyType=json, and carries the account's region — so new-api's
// Vertex adaptor authenticates via JWT against Cloud Billing (the only way the GCP
// trial credit is spendable; the Gemini Developer API ch24 prepay path does not accept it).
func TestNewAPIBridgeChannelInput_VertexServiceAccount(t *testing.T) {
	saJSON := `{"type":"service_account","project_id":"tk-proj","client_email":"x@tk-proj.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nAAA\n-----END PRIVATE KEY-----\n"}`
	account := &Account{
		ID:          9,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeServiceAccount,
		ChannelType: 41,
		Credentials: map[string]any{
			"base_url":             "https://us-central1-aiplatform.googleapis.com",
			"service_account_json": saJSON,
			"location":             "us-central1",
		},
	}
	in := newAPIBridgeChannelInput(account, 1, "google")
	if in.ChannelType != 41 {
		t.Fatalf("ChannelType: want 41, got %d", in.ChannelType)
	}
	if in.VertexKeyType != "json" {
		t.Fatalf("VertexKeyType: want json, got %q", in.VertexKeyType)
	}
	if in.VertexLocation != "us-central1" {
		t.Fatalf("VertexLocation: want us-central1, got %q", in.VertexLocation)
	}
	if in.APIKey != saJSON {
		t.Fatalf("APIKey must carry the service-account JSON, got %q", in.APIKey)
	}
}

func TestNewAPIBridgeChannelInputForBody_VertexUsesMappedModelLocation(t *testing.T) {
	saJSON := `{"type":"service_account","project_id":"tk-proj","client_email":"x@tk-proj.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nAAA\n-----END PRIVATE KEY-----\n"}`
	account := &Account{
		ID:          9,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeServiceAccount,
		ChannelType: 41,
		Credentials: map[string]any{
			"service_account_json": saJSON,
			"location":             "us-central1",
		},
	}

	in := newAPIBridgeChannelInputForBody(account, 1, "google", []byte(`{"model":"gemini-3.6-flash"}`))
	require.Equal(t, vertexGlobalLocation, in.VertexLocation)
}

// TestNewAPIBridgeChannelInput_NonVertexUntouched ensures a non-vertex account (ch24,
// plain api_key) gets no SA passthrough (VertexKeyType empty, api_key intact).
func TestNewAPIBridgeChannelInput_NonVertexUntouched(t *testing.T) {
	account := &Account{
		ID:          10,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 24,
		Credentials: map[string]any{"base_url": "https://generativelanguage.googleapis.com", "api_key": "sk-x"},
	}
	in := newAPIBridgeChannelInput(account, 1, "google")
	if in.VertexKeyType != "" {
		t.Fatalf("VertexKeyType must be empty for non-vertex, got %q", in.VertexKeyType)
	}
	if in.APIKey != "sk-x" {
		t.Fatalf("APIKey: want sk-x, got %q", in.APIKey)
	}
}
