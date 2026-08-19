//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

func TestAntigravityEffectiveDefaultModelMapping_EmptyRuntimeUsesCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	account := &Account{Platform: PlatformAntigravity}
	mapping := account.GetModelMapping()
	if mapping["gemini-3.6-flash"] != domain.DefaultAntigravityModelMapping["gemini-3.6-flash"] {
		t.Fatalf("compiled gemini-3.6-flash mapping = %q", mapping["gemini-3.6-flash"])
	}
	if !account.IsModelSupported("gemini-3.6-flash") {
		t.Fatal("compiled default must keep existing models servable")
	}
	if mapAntigravityModel(account, "gemini-9.9-flash") != "" {
		t.Fatal("unknown model must stay rejected without a runtime overlay")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_RuntimeOverlaysCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {
				"gemini-9.9-flash": "gemini-9.9-flash-medium",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}

	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{}}
	if got := mapAntigravityModel(account, "gemini-9.9-flash"); got != "gemini-9.9-flash-medium" {
		t.Fatalf("runtime overlay mapped %q, want gemini-9.9-flash-medium", got)
	}
	if !account.IsModelSupported("gemini-3.6-flash") {
		t.Fatal("partial runtime overlay must not drop compiled models")
	}
	if mapAntigravityModel(account, "gemini-3.6-flash") == "" {
		t.Fatal("compiled gemini-3.6-flash must remain mapped after overlay")
	}
}

func TestOverlayAntigravityDefaultModelMapping_RuntimeOverrideWinsOnConflict(t *testing.T) {
	compiled := domain.DefaultAntigravityModelMapping["gemini-3.6-flash"]
	if compiled == "" {
		t.Fatal("compiled default must contain gemini-3.6-flash")
	}
	got := overlayAntigravityDefaultModelMapping(map[string]string{
		"gemini-3.6-flash": "gemini-3.6-flash-override",
	})
	if got["gemini-3.6-flash"] != "gemini-3.6-flash-override" {
		t.Fatalf("runtime override mapped %q", got["gemini-3.6-flash"])
	}
	if got["gemini-3.5-flash"] != domain.DefaultAntigravityModelMapping["gemini-3.5-flash"] {
		t.Fatal("override of one key must keep other compiled keys")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_ExplicitAccountMappingWins(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {
				"gemini-9.9-flash": "gemini-9.9-flash-medium",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}

	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gemini-3.6-flash": "gemini-3.6-flash-tiered",
			},
		},
	}
	if account.IsModelSupported("gemini-9.9-flash") {
		t.Fatal("non-empty account mapping must not inherit runtime extras")
	}
	if !account.IsModelSupported("gemini-3.6-flash") {
		t.Fatal("explicit account mapping must stay authoritative")
	}
	if account.IsModelSupported("gemini-3.5-flash") {
		t.Fatal("explicit account mapping must not be auto-filled from compiled or runtime defaults")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_CorruptRuntimeKeepsCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	good, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {"gemini-9.9-flash": "gemini-9.9-flash-medium"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(good), true
	}); err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return "{not-json", true
	}); err == nil {
		t.Fatal("corrupt blob must fail closed")
	}

	account := &Account{Platform: PlatformAntigravity}
	if got := mapAntigravityModel(account, "gemini-9.9-flash"); got != "gemini-9.9-flash-medium" {
		t.Fatalf("corrupt reload must keep last-known-good overlay, got %q", got)
	}
}

func TestAntigravityEffectiveDefaultModelMapping_ClearRuntimeReturnsCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {"gemini-9.9-flash": "gemini-9.9-flash-medium"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}
	account := &Account{Platform: PlatformAntigravity}
	first := account.GetModelMapping()
	if first["gemini-9.9-flash"] != "gemini-9.9-flash-medium" {
		t.Fatalf("setup overlay missing, got %q", first["gemini-9.9-flash"])
	}

	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return "", false
	}); err != nil {
		t.Fatal(err)
	}
	second := account.GetModelMapping()
	if _, ok := second["gemini-9.9-flash"]; ok {
		t.Fatal("clear-runtime must drop overlay keys and return to compiled default")
	}
	if !account.IsModelSupported("gemini-3.6-flash") {
		t.Fatal("compiled default must remain after clear-runtime")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_CacheInvalidatesOnRuntimeReload(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{}}
	if account.IsModelSupported("gemini-9.9-flash") {
		t.Fatal("pre-reload unknown model must be rejected")
	}

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {"gemini-9.9-flash": "gemini-9.9-flash-medium"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}
	if !account.IsModelSupported("gemini-9.9-flash") {
		t.Fatal("same account object must observe the overlay after runtime version bump")
	}
}
