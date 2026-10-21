package apicompat

import "strings"

// GeminiImageSize512 is the upstream wire value for the 0.5K / 512px tier.
// Client aliases are normalized before forwarding to an image-capable upstream.
const GeminiImageSize512 = "512"

// NormalizeGeminiImageSize maps client size spellings onto the Gemini wire
// vocabulary. Empty input stays empty so callers can apply their own default.
// Unknown values are returned unchanged so admission layers can reject them.
func NormalizeGeminiImageSize(size string) string {
	trimmed := strings.TrimSpace(size)
	if trimmed == "" {
		return ""
	}
	switch strings.ToUpper(trimmed) {
	case "0.5K", "512", "512PX":
		return GeminiImageSize512
	case "1K", "2K", "4K":
		return strings.ToUpper(trimmed)
	case "512X512":
		return GeminiImageSize512
	case "1024X1024":
		return "1K"
	case "2048X2048":
		return "2K"
	case "4096X4096":
		return "4K"
	default:
		return trimmed
	}
}

// IsSupportedGeminiImageSize reports whether size is a known Gemini imageSize
// tier after normalization (including empty, which callers default separately).
func IsSupportedGeminiImageSize(size string) bool {
	normalized := NormalizeGeminiImageSize(size)
	switch normalized {
	case "", GeminiImageSize512, "1K", "2K", "4K":
		return true
	default:
		return false
	}
}
