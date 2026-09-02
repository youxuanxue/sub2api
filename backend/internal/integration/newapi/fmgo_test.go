package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsFMGoBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		channelType int
		base        string
		want        bool
	}{
		{"ch54 api", newapiconstant.ChannelTypeDoubaoVideo, "https://api.fmgo.top", true},
		{"ch54 www", newapiconstant.ChannelTypeDoubaoVideo, "https://www.fmgo.top", true},
		{"ch54 www /v1", newapiconstant.ChannelTypeDoubaoVideo, "https://www.fmgo.top/v1", true},
		{"ch54 apex", newapiconstant.ChannelTypeDoubaoVideo, "https://fmgo.top", true},
		{"ch1 www", 1, "https://www.fmgo.top", false},
		{"ch1 api", 1, "https://api.fmgo.top", false},
		{"ch54 ark", newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com", false},
		{"empty", newapiconstant.ChannelTypeDoubaoVideo, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsFMGoBaseURL(tc.channelType, tc.base); got != tc.want {
				t.Fatalf("IsFMGoBaseURL(%d, %q) = %v, want %v", tc.channelType, tc.base, got, tc.want)
			}
		})
	}
}

func TestFMGoSeedanceUpstreamSKU(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		client     string
		resolution string
		duration   int
		want       string
		wantErr    bool
	}{
		{"defaults", FMGoSeedanceClientID, "", 0, "feimiao-v2-431-720p-15s", false},
		{"fast defaults", FMGoSeedanceFastClientID, "", 0, "feimiao-v2-431-fast-720p-15s", false},
		{"explicit", FMGoSeedanceClientID, "720p", 10, "feimiao-v2-431-720p-10s", false},
		{"480p 15s", FMGoSeedanceClientID, "480p", 15, "feimiao-v2-431-480p-15s", false},
		{"reject 6s", FMGoSeedanceClientID, "720p", 6, "", true},
		{"reject 8s", FMGoSeedanceClientID, "480p", 8, "", true},
		{"reject 12s", FMGoSeedanceFastClientID, "720p", 12, "", true},
		{"reject 1080p", FMGoSeedanceClientID, "1080p", 10, "", true},
		{"reject 4k", FMGoSeedanceClientID, "4k", 10, "", true},
		{"reject 9s", FMGoSeedanceClientID, "720p", 9, "", true},
		{"v2.5 defaults", FMGoSeedance25ClientID, "", 0, "feimiao-v2.5-720p-15s", false},
		{"v2.5 720p 30s", FMGoSeedance25ClientID, "720p", 30, "feimiao-v2.5-720p-30s", false},
		{"v2.5 480p 5s", FMGoSeedance25ClientID, "480p", 5, "feimiao-v2.5-480p-5s", false},
		{"v2.5 reject 1080p", FMGoSeedance25ClientID, "1080p", 15, "", true},
		{"v2.5 reject 720p 5s", FMGoSeedance25ClientID, "720p", 5, "", true},
		{"v2.5 reject 8s", FMGoSeedance25ClientID, "720p", 8, "", true},
		{"passthrough other", "doubao-seedance-1-5-pro-251015", "1080p", 5, "doubao-seedance-1-5-pro-251015", false},
		{"passthrough already sku", "feimiao-v2.5-720p-10s", "720p", 10, "feimiao-v2.5-720p-10s", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := FMGoSeedanceUpstreamSKU(tc.client, tc.resolution, tc.duration)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("sku = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFMGoUsesVideosDialect(t *testing.T) {
	t.Parallel()
	for _, model := range []string{
		FMGoSeedanceClientID,
		FMGoSeedanceFastClientID,
		FMGoSeedance25ClientID,
		"feimiao-v2.5-720p-15s",
		"feimiao-v2-431-720p-15s",
		"feimiao-v2-431-fast-480p-10s",
		"feimiao-v2-mini-720p-10s",
	} {
		if !FMGoUsesVideosDialect(model) {
			t.Fatalf("%q must use /v1/videos", model)
		}
	}
	if FMGoUsesVideosDialect("feimiao-v2-720p-15s") {
		t.Fatal("legacy v2 SKU must stay on chat completions")
	}
	if got := FMGoSubmitPath(FMGoSeedanceClientID); got != FMGoVideosPath {
		t.Fatalf("seedance submit path = %q", got)
	}
	if !IsFMGoVideoInventoryID("feimiao-v2.5-720p-15s") || IsFMGoVideoInventoryID("claude-opus-4-8") {
		t.Fatal("video inventory classifier drifted")
	}
	if IsFMGoVideoInventoryID("veo-3-fast-4k-8s") || IsFMGoVideoInventoryID("grok-video-3") {
		t.Fatal("unsupported video families must not look probeable")
	}
}

func TestNormalizeFMGoBaseURL(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://api.fmgo.top",
		"https://api.fmgo.top/v1",
		"https://www.fmgo.top",
		"https://www.fmgo.top/v1/",
		"https://fmgo.top",
	} {
		if got := NormalizeFMGoBaseURL(raw); got != FMGoBaseURL {
			t.Fatalf("NormalizeFMGoBaseURL(%q) = %q, want %q", raw, got, FMGoBaseURL)
		}
	}
	if got := NormalizeFMGoBaseURL("https://ark.cn-beijing.volces.com"); got != "https://ark.cn-beijing.volces.com" {
		t.Fatalf("non-FMGo host must pass through, got %q", got)
	}
}

func TestNormalizeFMGoAspectRatio(t *testing.T) {
	t.Parallel()
	if got := NormalizeFMGoAspectRatio("9:16"); got != "9:16" {
		t.Fatalf("legal ratio = %q", got)
	}
	if got := NormalizeFMGoAspectRatio(""); got != "16:9" {
		t.Fatalf("empty ratio must default to 16:9, got %q", got)
	}
	if got := NormalizeFMGoAspectRatio("21:9"); got != "16:9" {
		t.Fatalf("unknown ratio must default to 16:9, got %q", got)
	}
}

func TestParseFMGoVideoDuration(t *testing.T) {
	t.Parallel()
	got, err := ParseFMGoVideoDuration("15")
	if err != nil || got != 15 {
		t.Fatalf("ParseFMGoVideoDuration(15) = %d, %v", got, err)
	}
	got, err = ParseFMGoVideoDuration("10s")
	if err != nil || got != 10 {
		t.Fatalf("ParseFMGoVideoDuration(10s) = %d, %v", got, err)
	}
	got, err = ParseFMGoVideoDuration("")
	if err != nil || got != 0 {
		t.Fatalf("empty duration should be 0, got %d %v", got, err)
	}
	if _, err = ParseFMGoVideoDuration("abc"); err == nil {
		t.Fatal("expected error for non-integer duration")
	}
}

func TestFMGoModelFamily_Official25IsV25Not431(t *testing.T) {
	t.Parallel()
	if got := FMGoModelFamily(FMGoSeedance25ClientID); got != FMGoFamilyV25 {
		t.Fatalf("official 2.5 family = %q, want %s", got, FMGoFamilyV25)
	}
	if got := FMGoModelFamily(FMGoSeedanceClientID); got != FMGoFamily431 {
		t.Fatalf("official 2.0 family = %q, want %s", got, FMGoFamily431)
	}
}

func TestFMGoClientForUpstreamSKU(t *testing.T) {
	t.Parallel()
	cases := []struct {
		upstream string
		want     string
	}{
		{"feimiao-v2-431-720p-15s", FMGoSeedanceClientID},
		{"feimiao-v2-431-fast-480p-10s", FMGoSeedanceFastClientID},
		{"feimiao-v2.5-720p-30s", FMGoSeedance25ClientID},
		{"feimiao-v2.5-480p-5s", FMGoSeedance25ClientID},
		{"feimiao-v2-720p-15s", FMGoSeedanceClientID},
		{"claude-opus-4-8", "claude-opus-4-8"},
	}
	for _, tc := range cases {
		t.Run(tc.upstream, func(t *testing.T) {
			t.Parallel()
			if got := FMGoClientForUpstreamSKU(tc.upstream); got != tc.want {
				t.Fatalf("FMGoClientForUpstreamSKU(%q) = %q, want %q", tc.upstream, got, tc.want)
			}
		})
	}
}
