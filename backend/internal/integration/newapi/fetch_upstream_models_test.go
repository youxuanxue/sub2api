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
	if len(models) != 1 || models[0] != "m1" {
		t.Fatalf("models = %#v", models)
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
