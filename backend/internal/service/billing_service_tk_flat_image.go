package service

import "strings"

// tkIsFlatPerImageModel reports whether `model` bills at a FLAT official
// per-image price with NO resolution-tier multiplier.
//
// Pixel dimensions on the wire do not imply resolution-tier pricing. Keep
// exemptions scoped to models with a verified flat official price.
func tkIsFlatPerImageModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	// Google Imagen: flat $/image per quality variant (no 1K/2K/4K generation tier).
	if strings.HasPrefix(m, "imagen-") {
		return true
	}
	// Ali Wan 2.7 (Token Plan / DashScope): official price is CNY per successful
	// image with no resolution-tier multiplier in the published list.
	if strings.HasPrefix(m, "wan2.7-image") {
		return true
	}
	// Agent Plan Seedream Lite: CNY 0.22 per successful image at every size.
	if m == "doubao-seedream-5.0-lite" {
		return true
	}
	return false
}

// tkApplyDefaultImageSizeMultiplier preserves existing size tiers except for
// verified flat-priced models. Settlement and pre-flight holds share this path.
func tkApplyDefaultImageSizeMultiplier(model string, imageSize string, basePrice float64) float64 {
	if tkIsFlatPerImageModel(model) {
		return basePrice
	}
	if imageSize == "2K" {
		return basePrice * 1.5
	}
	if imageSize == "4K" {
		return basePrice * 2
	}
	return basePrice
}
