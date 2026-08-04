//go:build unit

package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func resetPricingRegistrySnapshot(t *testing.T) {
	t.Helper()
	rebuildTKOverlayUnion(nil)
	t.Cleanup(func() { rebuildTKOverlayUnion(nil) })
}

func registryEnvelopeForTest(t *testing.T, mutate func(map[string]any), metadataMutate func(*tkPricingRegistrySnapshotMetadata)) string {
	t.Helper()
	var registry map[string]any
	require.NoError(t, json.Unmarshal(tkPricingOverlayRaw, &registry))
	if mutate != nil {
		mutate(registry)
	}
	registryBytes, err := json.Marshal(registry)
	require.NoError(t, err)
	sum := sha256.Sum256(registryBytes)
	metadata := tkPricingRegistrySnapshotMetadata{
		SchemaVersion:  1,
		SourceCommit:   strings.Repeat("a", 40),
		RegistrySHA256: hex.EncodeToString(sum[:]),
	}
	if metadataMutate != nil {
		metadataMutate(&metadata)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, err = zw.Write(registryBytes)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	envelope, err := json.Marshal(tkPricingRegistryRuntimeEnvelope{
		Snapshot:           metadata,
		RegistryGzipBase64: base64.StdEncoding.EncodeToString(compressed.Bytes()),
	})
	require.NoError(t, err)
	return string(envelope)
}

func TestUS042_RegistryReplacesExternalPricing(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.5":{"mode":"chat","litellm_provider":"openai",
		"input_cost_per_token":0.99,"output_cost_per_token":0.99},
		"provider-only":{"mode":"chat","litellm_provider":"sensor",
		"input_cost_per_token":0.1,"output_cost_per_token":0.2}
	}`))
	require.NoError(t, err)
	require.NotContains(t, data, "provider-only")
	require.InDelta(t, 5e-6, data["gpt-5.5"].InputCostPerToken, 1e-15)
	require.InDelta(t, 30e-6, data["gpt-5.5"].OutputCostPerToken, 1e-15)
}

func TestUS042_RuntimeSnapshotAtomicallyReplacesRegistry(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	const removed = "qwen3-32b"
	envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		delete(registry, removed)
		row := registry["qwen3-8b"].(map[string]any)
		row["input_cost_per_token"] = 9e-6
		config := registry["_config"].(map[string]any)
		config["web_search_price_per_call"] = 0.02
	}, nil)

	rebuildTKOverlayUnion([]byte(envelope))
	snapshot := loadTKPricingOverlaySnapshot()
	require.Equal(t, strings.Repeat("a", 40), snapshot.Metadata.SourceCommit)
	require.InDelta(t, 9e-6, snapshot.Models["qwen3-8b"].InputCostPerToken, 1e-15)
	require.NotContains(t, snapshot.Models, removed, "runtime replacement must not union embedded rows")
	require.InDelta(t, 0.02, snapshot.WebSearchPrice, 1e-15)
}

func TestUS042_InvalidAndLegacyRuntimeKeepLastKnownGood(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	good := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["qwen3-8b"].(map[string]any)["input_cost_per_token"] = 8e-6
	}, nil)
	rebuildTKOverlayUnion([]byte(good))

	cases := map[string]string{
		"legacy raw overlay": `{"qwen3-8b":{"input_cost_per_token":1}}`,
		"corrupt json":       `{broken`,
		"bad digest": registryEnvelopeForTest(t, nil, func(metadata *tkPricingRegistrySnapshotMetadata) {
			metadata.RegistrySHA256 = strings.Repeat("0", 64)
		}),
		"unsupported schema": registryEnvelopeForTest(t, nil, func(metadata *tkPricingRegistrySnapshotMetadata) {
			metadata.SchemaVersion = 2
		}),
	}
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			rebuildTKOverlayUnion([]byte(blob))
			snapshot := loadTKPricingOverlaySnapshot()
			require.InDelta(t, 8e-6, snapshot.Models["qwen3-8b"].InputCostPerToken, 1e-15)
			require.Equal(t, strings.Repeat("a", 40), snapshot.Metadata.SourceCommit)
		})
	}
}

func TestReloadTKOverlayRuntimeIsHashGatedAndInvalidatesCatalog(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	blob := registryEnvelopeForTest(t, nil, nil)
	svc := NewPricingService(&config.Config{}, nil)
	invalidations := 0
	svc.SetOverlayRuntimeDeps(func(context.Context) (string, bool) { return blob, true }, func() {
		invalidations++
	})
	changed, err := svc.reloadTKOverlayRuntime(context.Background())
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 1, invalidations)
	changed, err = svc.reloadTKOverlayRuntime(context.Background())
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, 1, invalidations)
}

func TestReloadTKOverlayRuntimeRejectsLegacyWithoutChangingHash(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	svc := NewPricingService(&config.Config{}, nil)
	current := registryEnvelopeForTest(t, nil, nil)
	svc.SetOverlayRuntimeDeps(func(context.Context) (string, bool) { return current, true }, nil)
	_, err := svc.reloadTKOverlayRuntime(context.Background())
	require.NoError(t, err)
	goodHash := svc.overlayRuntimeHash
	current = `{"legacy":{}}`
	changed, err := svc.reloadTKOverlayRuntime(context.Background())
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, goodHash, svc.overlayRuntimeHash)
}

func TestReloadTKOverlayRuntimeKeepsLKGWhenSettingTemporarilyDisappears(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	svc := NewPricingService(&config.Config{}, nil)
	present := true
	current := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["qwen3-8b"].(map[string]any)["input_cost_per_token"] = 8e-6
	}, nil)
	svc.SetOverlayRuntimeDeps(func(context.Context) (string, bool) {
		return current, present
	}, nil)

	changed, err := svc.reloadTKOverlayRuntime(context.Background())
	require.NoError(t, err)
	require.True(t, changed)
	goodHash := svc.overlayRuntimeHash

	present = false
	changed, err = svc.reloadTKOverlayRuntime(context.Background())
	require.ErrorContains(t, err, "keeping last-known-good")
	require.False(t, changed)
	require.Equal(t, goodHash, svc.overlayRuntimeHash)
	require.InDelta(t, 8e-6, loadTKPricingOverlaySnapshot().Models["qwen3-8b"].InputCostPerToken, 1e-15)
}

func TestReloadTKOverlayRuntimeSerializesGetterThroughSwap(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	svc := NewPricingService(&config.Config{}, nil)
	oldEnvelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["qwen3-8b"].(map[string]any)["input_cost_per_token"] = 7e-6
	}, nil)
	newEnvelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["qwen3-8b"].(map[string]any)["input_cost_per_token"] = 8e-6
	}, nil)
	firstGetterEntered := make(chan struct{})
	releaseFirstGetter := make(chan struct{})
	secondReloadStarted := make(chan struct{})
	var getterCalls int
	svc.SetOverlayRuntimeDeps(func(context.Context) (string, bool) {
		getterCalls++
		if getterCalls == 1 {
			close(firstGetterEntered)
			<-releaseFirstGetter
			return oldEnvelope, true
		}
		return newEnvelope, true
	}, nil)

	var wg sync.WaitGroup
	var firstErr, secondErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, firstErr = svc.reloadTKOverlayRuntime(context.Background())
	}()
	<-firstGetterEntered
	go func() {
		defer wg.Done()
		close(secondReloadStarted)
		_, secondErr = svc.reloadTKOverlayRuntime(context.Background())
	}()
	<-secondReloadStarted
	close(releaseFirstGetter)
	wg.Wait()

	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	require.Equal(t, 2, getterCalls)
	require.InDelta(t, 8e-6, loadTKPricingOverlaySnapshot().Models["qwen3-8b"].InputCostPerToken, 1e-15)
}

func TestConcurrentRegistryReadDuringExactSwap(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	a := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["qwen3-8b"].(map[string]any)["input_cost_per_token"] = 7e-6
	}, nil)
	b := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["qwen3-8b"].(map[string]any)["input_cost_per_token"] = 8e-6
	}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				snapshot := loadTKPricingOverlaySnapshot()
				require.NotNil(t, snapshot.Models["qwen3-8b"])
				require.Positive(t, snapshot.WebSearchPrice)
			}
		}()
	}
	for i := 0; i < 20; i++ {
		rebuildTKOverlayUnion([]byte(a))
		rebuildTKOverlayUnion([]byte(b))
	}
	wg.Wait()
}

func TestEmbeddedRegistryParsesAsCompleteFloor(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	snapshot, err := buildTKPricingOverlaySnapshot(nil)
	require.NoError(t, err)
	require.Len(t, snapshot.Models, 340)
	require.Equal(t, "embedded", snapshot.Metadata.SourceCommit)
	require.InDelta(t, 0.01, snapshot.WebSearchPrice, 1e-15)
	require.True(t, snapshot.Models["glm-4.5-flash"].ExplicitFree)
	require.NotContains(t, snapshot.Models, "deepseek-v3-2-251201")
}

func TestParseTKOverlayDocument_RejectsMalformedVideoTiers(t *testing.T) {
	validTier := `{"resolution":"720p","output_cost_per_second":0.1,"default_for_model":true}`
	tests := map[string]string{
		"empty tiers":              `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[]}}`,
		"unknown resolution":       `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"banana","output_cost_per_second":0.1,"default_for_model":true}]}}`,
		"noncanonical resolution":  `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"720P","output_cost_per_second":0.1,"default_for_model":true}]}}`,
		"duplicate resolution":     `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[` + validTier + `,` + validTier + `]}}`,
		"missing rate":             `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"720p","default_for_model":true}]}}`,
		"missing default":          `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"720p","output_cost_per_second":0.1}]}}`,
		"multiple defaults":        `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[` + validTier + `,{"resolution":"1080p","output_cost_per_second":0.2,"default_for_model":true}]}}`,
		"mismatched default field": `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"default_video_resolution":"1080p","video_price_tiers":[` + validTier + `]}}`,
		"flat not minimum":         `{"video":{"mode":"video_generation","output_cost_per_second":0.2,"video_price_tiers":[` + validTier + `]}}`,
	}
	for name, blob := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseTKOverlayDocument([]byte(blob))
			require.Error(t, err)
		})
	}
}

func TestParseTKOverlayDocument_DerivesVideoDefaultFromTierOwner(t *testing.T) {
	doc, err := parseTKOverlayDocument([]byte(`{
		"video": {
			"mode": "video_generation",
			"output_cost_per_second": 0.1,
			"video_price_tiers": [
				{"resolution":"720p","output_cost_per_second":0.1},
				{"resolution":"1080p","output_cost_per_second":0.2,"default_for_model":true}
			]
		}
	}`))
	require.NoError(t, err)
	require.Equal(t, "1080p", doc.Models["video"].DefaultVideoResolution)
}
