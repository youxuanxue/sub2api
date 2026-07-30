import { describe, expect, it } from 'vitest'
import {
  formatCatalogVideoHeadline,
  resolveVideoPricingVariant,
  type VideoPricingVariantTranslate,
} from '../videoPricingVariants.tk'

const t: VideoPricingVariantTranslate = (key, params) => {
  const map: Record<string, string> = {
    'pricing.variant.tierRange': '{lo}–{hi}',
    'pricing.video.withAudio': 'with audio',
    'pricing.video.withoutAudio': 'silent',
    'pricing.video.textToVideo': 'text-to-video',
    'pricing.video.withInputImage': 'image-to-video',
  }
  let out = map[key] ?? key
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      out = out.replace(`{${k}}`, String(v))
    }
  }
  return out
}

describe('videoPricingVariants', () => {
  it('returns flat view when video_price_tiers is absent', () => {
    const view = resolveVideoPricingVariant({ output_cost_per_second: 0.5 }, t)
    expect(view.kind).toBe('flat')
    expect(view.lines).toHaveLength(0)
    expect(view.minPerSecond).toBe(0.5)
    expect(formatCatalogVideoHeadline(view, t)).toBe('$0.5000')
  })

  it('builds range headline and per-tier lines for resolution×audio ladders', () => {
    const view = resolveVideoPricingVariant(
      {
        output_cost_per_second: 0.15,
        video_price_tiers: [
          { resolution: '720p', per_second: 0.4, per_second_silent: 0.2, default_for_model: true },
          { resolution: '1080p', per_second: 0.6, per_second_silent: 0.4 },
        ],
      },
      t,
    )
    expect(view.kind).toBe('tiered')
    expect(view.minPerSecond).toBe(0.2)
    expect(view.maxPerSecond).toBe(0.6)
    expect(formatCatalogVideoHeadline(view, t)).toBe('$0.2000–$0.6000')
    expect(view.lines.map((l) => l.label)).toEqual([
      '720p · with audio',
      '720p · silent',
      '1080p · with audio',
      '1080p · silent',
    ])
  })

  it('includes image-input surcharge as its own line for Grok-style tiers', () => {
    const view = resolveVideoPricingVariant(
      {
        output_cost_per_second: 0.05,
        video_price_tiers: [
          {
            resolution: '720p',
            per_second: 0.05,
            input_image_surcharge_per_second: 0.01,
            default_for_model: true,
          },
        ],
      },
      t,
    )
    expect(view.lines).toHaveLength(2)
    expect(view.lines[1].label).toBe('720p · image-to-video')
    expect(view.lines[1].perSecond).toBeCloseTo(0.06)
  })
})
