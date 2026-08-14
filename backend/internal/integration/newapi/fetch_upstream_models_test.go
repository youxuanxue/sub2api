package newapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestUpstreamModelFetchAllowed_OpenRouter(t *testing.T) {
	t.Parallel()
	if !UpstreamModelFetchAllowed(newapiconstant.ChannelTypeOpenRouter) {
		t.Fatal("expected OpenRouter to allow upstream model fetch")
	}
}

func TestIsKnownChannelType(t *testing.T) {
	t.Parallel()
	if !IsKnownChannelType(20) {
		t.Fatal("expected 20 to be a known channel type")
	}
	if IsKnownChannelType(0) || IsKnownChannelType(newapiconstant.ChannelTypeDummy) {
		t.Fatal("expected invalid types to be rejected")
	}
}

func TestFetchUpstreamModelList_TrimsTrailingV1FromBase(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "m1"}},
		})
	}))
	t.Cleanup(ts.Close)

	baseWithV1 := ts.URL + "/v1"
	models, err := FetchUpstreamModelList(context.Background(), baseWithV1, newapiconstant.ChannelTypeMoonshot, "sk-test")
	if err != nil {
		t.Fatalf("FetchUpstreamModelList: %v", err)
	}
	if len(models) != 1 || models[0].ID != "m1" {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ProviderUnavailable {
		t.Fatalf("default provider response must not flag unavailable: %#v", models[0])
	}
}

func TestFetchUpstreamModelList_UsesDashScopeCompatibleModelsPath(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compatible-mode/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "qwen3.7-max"}},
		})
	}))
	t.Cleanup(ts.Close)

	models, err := FetchUpstreamModelList(context.Background(), ts.URL, newapiconstant.ChannelTypeAli, "sk-test")
	if err != nil {
		t.Fatalf("FetchUpstreamModelList: %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen3.7-max" {
		t.Fatalf("models = %#v", models)
	}
}

func TestVolcEngineModelsURL_ResolvesXRTokenPublicCatalog(t *testing.T) {
	t.Parallel()

	for _, base := range []string{XRTokenBaseURL, XRTokenBaseURL + "/v1"} {
		got, err := volcEngineModelsURL(newapiconstant.ChannelTypeDoubaoVideo, base)
		if err != nil {
			t.Fatalf("volcEngineModelsURL(%q): %v", base, err)
		}
		if got != XRTokenBaseURL+"/v1/models" {
			t.Fatalf("volcEngineModelsURL(%q) = %q", base, got)
		}
	}
}

func TestVolcEngineModelsURL_ResolvesAgentPlanSpecialBase(t *testing.T) {
	t.Parallel()

	got, err := volcEngineModelsURL(newapiconstant.ChannelTypeVolcEngine, VolcEngineAgentPlanBaseURL)
	if err != nil {
		t.Fatalf("volcEngineModelsURL: %v", err)
	}
	want := VolcEngineAgentPlanBaseURL + "/models"
	if got != want {
		t.Fatalf("volcEngineModelsURL = %q, want %q", got, want)
	}
}

func TestMoonshotAlternateRegionalBase(t *testing.T) {
	t.Parallel()
	if g := MoonshotAlternateRegionalBase("https://api.moonshot.cn"); g != "https://api.moonshot.ai" {
		t.Fatalf("cn->ai: got %q", g)
	}
	if cn := MoonshotAlternateRegionalBase("https://api.moonshot.ai"); cn != "https://api.moonshot.cn" {
		t.Fatalf("ai->cn: got %q", cn)
	}
	if MoonshotAlternateRegionalBase("https://api.deepseek.com") != "" {
		t.Fatal("expected empty for non-moonshot host")
	}
}

func TestShouldResolveMoonshotBaseURLAtSave(t *testing.T) {
	t.Parallel()
	if !ShouldResolveMoonshotBaseURLAtSave("") {
		t.Fatal("empty base should resolve")
	}
	if !ShouldResolveMoonshotBaseURLAtSave("https://api.moonshot.cn") {
		t.Fatal("cn official should resolve")
	}
	if ShouldResolveMoonshotBaseURLAtSave("https://relay.example.com") {
		t.Fatal("custom proxy must not auto-resolve")
	}
}
