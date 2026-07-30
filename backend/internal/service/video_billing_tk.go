package service

import (
	"strings"
)

// VideoBillingOptions carries optional dimensions for tiered video billing.
// Nil GenerateAudio uses each model's upstream default (typically with audio).
type VideoBillingOptions struct {
	GenerateAudio *bool
	HasInputImage bool
}

func videoBillingWithAudio(opts *VideoBillingOptions, defaultWithAudio bool) bool {
	if opts != nil && opts.GenerateAudio != nil {
		return *opts.GenerateAudio
	}
	return defaultWithAudio
}

func videoBillingHasInputImage(opts *VideoBillingOptions) bool {
	return opts != nil && opts.HasInputImage
}

// Video billing for tiered models reads tk_pricing_overlay.json exclusively
// (video_price_tiers). See video_billing_overlay_tk.go.

func tkVideoDefaultResolution(model string) string {
	if tkIsTieredVideoModel(model) {
		return tkOverlayVideoDefaultResolution(model)
	}
	return VideoBillingResolution480P
}

func tkVideoNormalizeResolution(model, resolution string) string {
	if strings.TrimSpace(resolution) == "" {
		return tkVideoDefaultResolution(model)
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	if tkIsTieredVideoModel(model) {
		if tkOverlayVideoSupportsResolution(model, resolution) {
			return resolution
		}
		return tkOverlayVideoDefaultResolution(model)
	}
	return resolution
}

func tkVideoUnitPriceUSD(model, resolution string, opts *VideoBillingOptions) (float64, bool) {
	if price, ok := tkOverlayVideoUnitPriceUSD(model, resolution, opts); ok {
		return price, true
	}
	return 0, false
}

func tkVideoHoldUnitPriceUSD(model string) float64 {
	return tkOverlayVideoHoldUnitPriceUSD(model)
}

func tkVideoMinUnitPriceUSD(model string) (float64, bool) {
	return tkOverlayVideoMinUnitPriceUSD(model)
}
