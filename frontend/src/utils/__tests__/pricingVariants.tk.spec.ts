import { describe, expect, it } from 'vitest'

import {
  FLAT_PRICING_VARIANT,
  formatTierLabel,
  formatTokenBound,
  resolvePricingVariant,
  type PricingVariantTier
} from '../pricingVariants.tk'

/**
 * Stand-in for vue-i18n's `t`. Renders the key plus its params so assertions
 * read against structure, not against a locale string that may be retuned.
 */
const t = (key: string, params?: Record<string, unknown>): string => {
  const short = key.replace('pricing.variant.', '')
  if (!params) return short
  const rendered = Object.entries(params)
    .map(([k, v]) => `${k}=${String(v)}`)
    .join(' ')
  return `${short}(${rendered})`
}

const flat = { inputPer1K: 0.001, outputPer1K: 0.002, cacheReadPer1K: 0.0001 }

function tier(
  minTokens: number,
  maxTokens: number | null,
  inputPer1K: number,
  outputPer1K: number,
  cacheReadPer1K: number | null = null
): PricingVariantTier {
  return { minTokens, maxTokens, inputPer1K, outputPer1K, cacheReadPer1K }
}

describe('formatTokenBound', () => {
  it('compacts round thousands and millions', () => {
    expect(formatTokenBound(32000)).toBe('32k')
    expect(formatTokenBound(256000)).toBe('256k')
    expect(formatTokenBound(1000000)).toBe('1M')
  })

  it('leaves non-round values raw', () => {
    expect(formatTokenBound(1500)).toBe('1500')
  })
})

describe('formatTierLabel', () => {
  // Billing matches (min, max] — FindMatchingInterval in channel.go. The labels
  // must say that, so a 32000-token request reads as landing in the FIRST
  // bracket (which is what it is billed at), not the second.
  it('renders an open floor as "up to max"', () => {
    expect(formatTierLabel({ minTokens: 0, maxTokens: 32000 }, t)).toBe('tierUpTo(bound=32k)')
  })

  it('renders an open ceiling as "above min"', () => {
    expect(formatTierLabel({ minTokens: 128000, maxTokens: null }, t)).toBe('tierAbove(bound=128k)')
  })

  it('renders a bounded bracket as a range', () => {
    expect(formatTierLabel({ minTokens: 32000, maxTokens: 128000 }, t)).toBe(
      'tierRange(lo=32k hi=128k)'
    )
  })

  it('returns empty for a bracket unbounded on both ends', () => {
    expect(formatTierLabel({ minTokens: 0, maxTokens: null }, t)).toBe('')
  })
})

describe('resolvePricingVariant', () => {
  it('is flat when there is no ladder and no peak policy', () => {
    expect(resolvePricingVariant({ flat }, t)).toEqual(FLAT_PRICING_VARIANT)
  })

  it('lists every tier as its own line, in order, with per-tier cache price', () => {
    const view = resolvePricingVariant(
      {
        flat,
        tiers: [
          tier(0, 32000, 0.000478, 0.002388, 0.0000955),
          tier(32000, 128000, 0.000716, 0.003582, 0.000143),
          tier(128000, null, 0.001433, 0.007164, 0.000287)
        ]
      },
      t
    )
    expect(view.kind).toBe('tiered')
    expect(view.lines.map((l) => l.label)).toEqual([
      'tierUpTo(bound=32k)',
      'tierRange(lo=32k hi=128k)',
      'tierAbove(bound=128k)'
    ])
    // The top tier is 3× the first — the number the page used to show alone.
    expect(view.lines[2].inputPer1K).toBeCloseTo(0.001433, 9)
    expect(view.lines[0].cacheReadPer1K).toBeCloseTo(0.0000955, 9)
    expect(view.caption).toBe('tieredCaption')
  })

  it('treats a single-bracket ladder as flat', () => {
    const view = resolvePricingVariant({ flat, tiers: [tier(0, 32000, 0.001, 0.002)] }, t)
    expect(view.kind).toBe('flat')
    expect(view.lines).toHaveLength(0)
  })

  it('puts off-peak first and peak second, with the flat price as off-peak', () => {
    const view = resolvePricingVariant(
      {
        flat,
        peakValley: {
          timezone: 'Asia/Shanghai',
          windows: ['09:00-12:00', '14:00-18:00'],
          peakMultiplier: 2,
          inputPer1K: 0.002,
          outputPer1K: 0.004,
          cacheReadPer1K: 0.0002
        }
      },
      t
    )
    expect(view.kind).toBe('peak_valley')
    expect(view.lines).toHaveLength(2)
    expect(view.lines[0]).toEqual({
      label: 'offPeak',
      inputPer1K: 0.001,
      outputPer1K: 0.002,
      cacheReadPer1K: 0.0001
    })
    expect(view.lines[1]).toEqual({
      label: 'peak',
      inputPer1K: 0.002,
      outputPer1K: 0.004,
      cacheReadPer1K: 0.0002
    })
    // Windows are en-dashed for display and the timezone is always stated —
    // a bare "09:00-12:00" would be read in the visitor's own timezone.
    expect(view.caption).toBe(
      'peakCaption(windows=09:00–12:00, 14:00–18:00 tz=Asia/Shanghai mult=2)'
    )
  })

  it('ignores a peak policy with multiplier <= 1 or no windows', () => {
    const base = {
      timezone: 'Asia/Shanghai',
      windows: ['09:00-12:00'],
      peakMultiplier: 1,
      inputPer1K: 0.001,
      outputPer1K: 0.002,
      cacheReadPer1K: null
    }
    expect(resolvePricingVariant({ flat, peakValley: base }, t).kind).toBe('flat')
    expect(
      resolvePricingVariant({ flat, peakValley: { ...base, peakMultiplier: 2, windows: [] } }, t).kind
    ).toBe('flat')
  })

  it('prefers the ladder when a model somehow has both', () => {
    // No overlay entry is both tiered and peak-priced today (a backend test
    // guards that); rendering the 3×2 cross-product would be unreadable, so the
    // ladder wins rather than producing six lines.
    const view = resolvePricingVariant(
      {
        flat,
        tiers: [tier(0, 32000, 0.001, 0.002), tier(32000, null, 0.002, 0.004)],
        peakValley: {
          timezone: 'Asia/Shanghai',
          windows: ['09:00-12:00'],
          peakMultiplier: 2,
          inputPer1K: 0.002,
          outputPer1K: 0.004,
          cacheReadPer1K: null
        }
      },
      t
    )
    expect(view.kind).toBe('tiered')
    expect(view.lines).toHaveLength(2)
  })

  it('keeps every line shaped for aligned rendering across price columns', () => {
    // Consumers render one visual line per entry in each of the input, output
    // and cache columns; a missing price must still occupy its line.
    const view = resolvePricingVariant(
      { flat, tiers: [tier(0, 32000, 0.001, 0.002), tier(32000, null, 0.002, 0.004)] },
      t
    )
    for (const line of view.lines) {
      expect(line).toHaveProperty('label')
      expect(line).toHaveProperty('inputPer1K')
      expect(line).toHaveProperty('outputPer1K')
      expect(line).toHaveProperty('cacheReadPer1K')
    }
  })
})
