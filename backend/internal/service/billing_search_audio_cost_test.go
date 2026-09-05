package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateSearchCost(t *testing.T) {
	t.Parallel()
	s := &BillingService{}
	require.Equal(t, 0.0, s.CalculateSearchCost(0, floatPtr(10), 1).ActualCost)
	// nil price -> official xAI default $5/1k: 5 calls = 0.025
	require.InDelta(t, 0.025, s.CalculateSearchCost(5, nil, 1).ActualCost, 1e-9)
	// explicit 0 → free
	require.Equal(t, 0.0, s.CalculateSearchCost(5, floatPtr(0), 1).ActualCost)
	price := 10.0
	cost := s.CalculateSearchCost(100, &price, 1.5)
	// 10 / 1000 * 100 * 1.5 = 1.5
	require.InDelta(t, 1.0, cost.TotalCost, 1e-9)
	require.InDelta(t, 1.5, cost.ActualCost, 1e-9)
}

func TestCalculateAudioCost(t *testing.T) {
	t.Parallel()
	s := &BillingService{}
	rt, tts, stt := 0.10, 15.0, 0.50
	cfg := &audioPriceConfig{RealtimePerMin: &rt, TTSPerMChars: &tts, STTPerHour: &stt}
	require.InDelta(t, 0.20, s.CalculateAudioCost("realtime", 2, cfg, 1).ActualCost, 1e-9)
	require.InDelta(t, 1.5, s.CalculateAudioCost("tts", 0.1, cfg, 1).ActualCost, 1e-9)
	require.InDelta(t, 0.25, s.CalculateAudioCost("stt", 0.5, cfg, 1).ActualCost, 1e-9)
	require.Equal(t, 0.0, s.CalculateAudioCost("unknown", 1, cfg, 1).ActualCost)
	// nil config -> official defaults (think-fast-1 $0.05/min, TTS $15/M, REST STT $0.10/hr)
	require.InDelta(t, 0.05, s.CalculateAudioCost("realtime", 1, nil, 1).ActualCost, 1e-9)
	require.InDelta(t, 15.0, s.CalculateAudioCost("tts", 1, nil, 1).ActualCost, 1e-9)
	require.InDelta(t, 0.10, s.CalculateAudioCost("stt", 1, nil, 1).ActualCost, 1e-9)
	// explicit 0 → free
	zero := 0.0
	require.Equal(t, 0.0, s.CalculateAudioCost("realtime", 1, &audioPriceConfig{RealtimePerMin: &zero}, 1).ActualCost)
}

func TestCalculateAudioCostForModel_AliTTSOverlay(t *testing.T) {
	t.Parallel()
	s := &BillingService{}
	wantPerM := s.TkRegistryTTSPricePerMillionChars("qwen-audio-3.0-tts-plus")
	require.Greater(t, wantPerM, 0.0)
	got := s.CalculateAudioCostForModel("qwen-audio-3.0-tts-plus", "tts", 0.001, nil, 1.0)
	require.InDelta(t, wantPerM*0.001, got.ActualCost, 1e-12)
	require.False(t, s.TkTTSModelUnpriced("qwen-audio-3.0-tts-plus", nil))
	require.True(t, s.TkTTSModelUnpriced("totally-unknown-tts-model", nil))
}

func TestTkTTSModelUnpriced_GroupExplicitZeroIsPricedFree(t *testing.T) {
	t.Parallel()
	s := &BillingService{}
	zero := 0.0
	group := &Group{AudioTTSPricePerMillionChars: &zero}
	require.False(t, s.TkTTSModelUnpriced("totally-unknown-tts-model", group))
	cfg := &audioPriceConfig{TTSPerMChars: &zero}
	require.Equal(t, 0.0, s.CalculateAudioCostForModel("totally-unknown-tts-model", "tts", 0.01, cfg, 1).ActualCost)
}

func TestCalculateAudioCostForModel_GroupOverrideBeatsRegistry(t *testing.T) {
	t.Parallel()
	s := &BillingService{}
	registryPerM := s.TkRegistryTTSPricePerMillionChars("qwen-audio-3.0-tts-plus")
	require.Greater(t, registryPerM, 0.0)
	override := 42.0
	cfg := &audioPriceConfig{TTSPerMChars: &override}
	got := s.CalculateAudioCostForModel("qwen-audio-3.0-tts-plus", "tts", 0.01, cfg, 1.0)
	require.InDelta(t, 0.42, got.ActualCost, 1e-12)
	require.Greater(t, math.Abs(got.ActualCost-registryPerM*0.01), 1e-6)
}

func TestAliTTSBillingUnits_FromCharacterCount(t *testing.T) {
	t.Parallel()
	s := &BillingService{}
	chars := 10000 // 0.01 M chars
	units := float64(chars) / 1_000_000.0
	perM := s.TkRegistryTTSPricePerMillionChars("qwen-audio-3.0-tts-plus")
	require.Greater(t, perM, 0.0)
	hold := s.EstimateTTSHold("qwen-audio-3.0-tts-plus", chars, nil, 1.0)
	settle := s.CalculateAudioCostForModel("qwen-audio-3.0-tts-plus", "tts", units, nil, 1.0)
	require.InDelta(t, hold, settle.ActualCost, 1e-12)
	require.InDelta(t, perM*0.01, settle.ActualCost, 1e-12)
}

func floatPtr(v float64) *float64 { return &v }
