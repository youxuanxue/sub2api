//go:build unit

package newapi

import "testing"

func TestNormalizeVideoTaskResolution(t *testing.T) {
	tests := map[string]string{
		"sd":        VideoTaskResolution480P,
		"720p":      VideoTaskResolution720P,
		"full_hd":   VideoTaskResolution1080P,
		"2160p":     VideoTaskResolution4K,
		"1280x720":  VideoTaskResolution720P,
		"1920x1080": VideoTaskResolution1080P,
		"3840x2160": VideoTaskResolution4K,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, ok := NormalizeVideoTaskResolution(input)
			if !ok || got != want {
				t.Fatalf("NormalizeVideoTaskResolution(%q) = %q, %t; want %q, true", input, got, ok, want)
			}
		})
	}
	for _, input := range []string{"banana", "1920xnope", "0x0"} {
		t.Run("invalid_"+input, func(t *testing.T) {
			if got, ok := NormalizeVideoTaskResolution(input); ok {
				t.Fatalf("NormalizeVideoTaskResolution(%q) = %q, true; want invalid", input, got)
			}
		})
	}
}
