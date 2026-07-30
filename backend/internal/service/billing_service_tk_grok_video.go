package service

import (
	"math"
	"strings"
)

const grokImagineVideoImageInputSurchargePerSecond = 0.01

func tkIsGrokImagineVideoModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "grok-imagine-video")
}

func tkGrokImagineVideoUnitPriceUSD(model, resolution string, opts *VideoBillingOptions) (float64, bool) {
	price, ok := getDefaultGrokImagineVideoPrice(model, resolution)
	if !ok {
		return 0, false
	}
	if videoBillingHasInputImage(opts) && strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video") &&
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video-1.5") {
		price += grokImagineVideoImageInputSurchargePerSecond
	}
	return price, true
}

func tkGrokImagineVideoHoldUnitPriceUSD(model string) float64 {
	if !tkIsGrokImagineVideoModel(model) {
		return 0
	}
	max := 0.0
	for _, res := range []string{VideoBillingResolution480P, VideoBillingResolution720P, VideoBillingResolution1080P} {
		withImage := &VideoBillingOptions{HasInputImage: true}
		if p, ok := tkGrokImagineVideoUnitPriceUSD(model, res, withImage); ok {
			max = math.Max(max, p)
		}
		if p, ok := tkGrokImagineVideoUnitPriceUSD(model, res, nil); ok {
			max = math.Max(max, p)
		}
	}
	return max
}

func tkGrokImagineVideoMinUnitPriceUSD(model string) (float64, bool) {
	if p, ok := tkGrokImagineVideoUnitPriceUSD(model, VideoBillingResolution480P, nil); ok {
		return p, true
	}
	return 0, false
}

// GrokVideoCatalogTier is one resolution row for the public catalog.
type GrokVideoCatalogTier struct {
	Resolution                   string
	PerSecond                    float64
	InputImageSurchargePerSecond float64
	DefaultForModel              bool
}

func tkGrokImagineVideoCatalogTiers(model string) []GrokVideoCatalogTier {
	if !tkIsGrokImagineVideoModel(model) {
		return nil
	}
	resolutions := []string{VideoBillingResolution480P, VideoBillingResolution720P}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video-1.5") {
		resolutions = append(resolutions, VideoBillingResolution1080P)
	}
	out := make([]GrokVideoCatalogTier, 0, len(resolutions))
	for _, res := range resolutions {
		p, ok := tkGrokImagineVideoUnitPriceUSD(model, res, nil)
		if !ok {
			continue
		}
		surcharge := 0.0
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video-1.5") {
			surcharge = grokImagineVideoImageInputSurchargePerSecond
		}
		out = append(out, GrokVideoCatalogTier{
			Resolution:                   res,
			PerSecond:                    p,
			InputImageSurchargePerSecond: surcharge,
			DefaultForModel:              res == VideoBillingResolution480P,
		})
	}
	return out
}
