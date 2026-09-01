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
		{"passthrough other", "doubao-seedance-2-5-260628", "1080p", 5, "doubao-seedance-2-5-260628", false},
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
