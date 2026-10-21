package apicompat

import "testing"

func TestNormalizeGeminiImageSize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"0.5K", "512"},
		{"0.5k", "512"},
		{"05.K", "05.K"},
		{"512", "512"},
		{"512px", "512"},
		{" 512PX ", "512"},
		{"512X512", "512"},
		{"512x512", "512"},
		{"1K", "1K"},
		{"1k", "1K"},
		{"2K", "2K"},
		{"4K", "4K"},
		{"1024x1024", "1K"},
		{"2048x2048", "2K"},
		{"4096x4096", "4K"},
		{"3K", "3K"}, // unknown preserved for admission reject
	} {
		if got := NormalizeGeminiImageSize(tc.in); got != tc.want {
			t.Fatalf("NormalizeGeminiImageSize(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
	for _, ok := range []string{"", "0.5K", "512", "1K", "2K", "4K", "512x512"} {
		if !IsSupportedGeminiImageSize(ok) {
			t.Fatalf("IsSupportedGeminiImageSize(%q)=false", ok)
		}
	}
	for _, size := range []string{"3K", "05.K", "512x768", "512x512junk"} {
		if IsSupportedGeminiImageSize(size) {
			t.Fatalf("%q must not be supported", size)
		}
	}
}
