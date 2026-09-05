package service

import "strings"

// tkIsFlatPerImageModel reports whether `model` bills at a FLAT official
// per-image price with NO resolution-tier multiplier.
//
// Google prices Imagen (Vertex) as a single $/image per quality variant
// (Fast/Standard/Ultra) — there is NO 1K/2K/4K *generation* tier. The generic
// getDefaultImagePrice applies a 2K→×1.5 / 4K→×2 multiplier that is real only
// for models with genuine pixel-size tiers (Seedream/Doubao, which put WxH on
// the wire). Because Imagen requests carry ratio codes (no size), they default
// to the "2K" tier and were silently marked up ×1.5 — an artifact of an
// upstream-owned multiplier (Wei-Shaw/sub2api d1b684b78 "add 2K image default
// pricing at 1.5x base price"), not a TokenKey pricing decision. This predicate
// scopes the flat-billing exemption to Imagen so Seedream keeps its real
// size-tier multiplier.
//
// Prefix match (case-insensitive) covers every dated/quality Imagen variant
// (imagen-4.0-ultra-generate-001, imagen-3.0-generate-002, …) without enumerating
// ids; the only models named `imagen-*` are Google's flat-priced Imagen family.
func tkIsFlatPerImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "imagen-")
}

// tkApplyDefaultImageSizeMultiplier applies Seedream-style 2K/4K size tiers,
// except for flat-priced Imagen which must return basePrice unchanged.
func tkApplyDefaultImageSizeMultiplier(model string, imageSize string, basePrice float64) float64 {
	// TK: Imagen bills at its FLAT official per-image price. Google prices Imagen
	// as a single $/image per quality variant with NO 1K/2K/4K generation tier, so
	// the size-tier multiplier below (real only for genuine pixel-size tiers such
	// as Seedream) must not apply. Imagen requests carry ratio codes (no size) and
	// default to "2K", which silently marked them up ×1.5 — this exemption removes
	// that. Routes through the same function as the pre-flight HOLD, so settle and
	// hold stay consistent. See tkIsFlatPerImageModel.
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
