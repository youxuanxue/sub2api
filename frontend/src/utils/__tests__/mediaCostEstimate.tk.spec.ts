import { describe, it, expect } from 'vitest'
import {
  classifyImageBillingTier,
  estimateImageCost,
  estimateImageHoldCost,
  estimateVideoCost,
  formatUsd,
  IMAGE_SIZE_MULTIPLIER,
} from '@/utils/mediaCostEstimate.tk'

describe('classifyImageBillingTier (mirror of backend ClassifyImageBillingTier)', () => {
  it('maps literal tiers case-insensitively', () => {
    expect(classifyImageBillingTier('1K')).toBe('1K')
    expect(classifyImageBillingTier('2k')).toBe('2K')
    expect(classifyImageBillingTier('4K')).toBe('4K')
  })

  it('classifies pixel strings by max edge', () => {
    expect(classifyImageBillingTier('1024x1024')).toBe('1K')
    expect(classifyImageBillingTier('1536x1024')).toBe('2K') // maxEdge 1536 ≤ 2048
    expect(classifyImageBillingTier('1024x1536')).toBe('2K')
    expect(classifyImageBillingTier('3840x2160')).toBe('4K')
  })

  it('defaults empty / auto / unknown to 2K (no deceptive "auto = cheapest")', () => {
    expect(classifyImageBillingTier('')).toBe('2K')
    expect(classifyImageBillingTier('auto')).toBe('2K')
    expect(classifyImageBillingTier('garbage')).toBe('2K')
    expect(classifyImageBillingTier(undefined)).toBe('2K')
  })
})

describe('estimateImageCost (mirror of CalculateImageCost)', () => {
  it('Standard imagen 2K ×1.5 × 1 = $0.06 (design acceptance case)', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '2K', n: 1 })).toBeCloseTo(0.06, 10)
  })

  it('1K is ×1', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '1K', n: 1 })).toBeCloseTo(0.04, 10)
  })

  it('4K is ×2', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '4K', n: 1 })).toBeCloseTo(0.08, 10)
  })

  it('scales linearly by n', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '1K', n: 3 })).toBeCloseTo(0.12, 10)
  })

  it('applies rate multiplier (group override)', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '1K', n: 1, rateMultiplier: 1.5 })).toBeCloseTo(0.06, 10)
  })

  it('clamps negative rate to 0', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '1K', n: 1, rateMultiplier: -1 })).toBe(0)
  })

  it('treats n<=0 as 1', () => {
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '1K', n: 0 })).toBeCloseTo(0.04, 10)
  })

  it('falls back to $0.134 base when price missing', () => {
    expect(estimateImageCost({ baseImagePrice: 0, size: '1K', n: 1 })).toBeCloseTo(0.134, 10)
  })

  it('classifies a raw pixel size before pricing', () => {
    // 1536x1024 → 2K → ×1.5
    expect(estimateImageCost({ baseImagePrice: 0.04, size: '1536x1024', n: 1 })).toBeCloseTo(0.06, 10)
  })

  it('seedream sub-cent base prices correctly', () => {
    expect(estimateImageCost({ baseImagePrice: 0.029850746268656716, size: '1K', n: 1 })).toBeCloseTo(0.0298507, 6)
  })
})

describe('estimateImageHoldCost (mirror of backend pre-flight HOLD: 4K tier-max)', () => {
  it('reserves the 4K tier (base ×2) regardless of requested size', () => {
    // imagen-4 fast base $0.02 → hold $0.04 (4K = ×2), n=1.
    expect(estimateImageHoldCost({ baseImagePrice: 0.02, n: 1 })).toBeCloseTo(0.04, 10)
  })

  it('scales by n and rate', () => {
    expect(estimateImageHoldCost({ baseImagePrice: 0.06, n: 4, rateMultiplier: 1 })).toBeCloseTo(0.48, 10)
  })

  it('is always >= the per-size settlement estimate (never under-reserves)', () => {
    for (const size of ['1K', '2K', '4K'] as const) {
      const settle = estimateImageCost({ baseImagePrice: 0.04, size, n: 2 })
      const hold = estimateImageHoldCost({ baseImagePrice: 0.04, n: 2 })
      expect(hold).toBeGreaterThanOrEqual(settle - 1e-12)
    }
  })

  it('uses the $0.134 fallback base too', () => {
    expect(estimateImageHoldCost({ baseImagePrice: 0, n: 1 })).toBeCloseTo(0.268, 10)
  })
})

describe('estimateVideoCost (mirror of CalculateVideoCost)', () => {
  it('Cinematic Veo 8s × $0.40/s = $3.20 (design acceptance case)', () => {
    expect(estimateVideoCost({ perSecond: 0.4, seconds: 8 })).toBeCloseTo(3.2, 10)
  })

  it('Seedance 5s ≈ $0.544', () => {
    expect(estimateVideoCost({ perSecond: 0.10880597014925374, seconds: 5 })).toBeCloseTo(0.5440298, 6)
  })

  it('applies rate multiplier', () => {
    expect(estimateVideoCost({ perSecond: 0.4, seconds: 8, rateMultiplier: 2 })).toBeCloseTo(6.4, 10)
  })

  it('treats seconds<=0 as 1', () => {
    expect(estimateVideoCost({ perSecond: 0.4, seconds: 0 })).toBeCloseTo(0.4, 10)
  })

  it('returns 0 when unpriced', () => {
    expect(estimateVideoCost({ perSecond: 0, seconds: 8 })).toBe(0)
  })
})

describe('formatUsd', () => {
  it('formats sub-cent with 4 decimals', () => {
    expect(formatUsd(0.0298507)).toBe('$0.0299')
  })
  it('formats normal with 2 decimals', () => {
    expect(formatUsd(3.2)).toBe('$3.20')
    expect(formatUsd(0.06)).toBe('$0.06')
  })
  it('renders zero as $0', () => {
    expect(formatUsd(0)).toBe('$0')
  })
  it('renders non-finite as em dash', () => {
    expect(formatUsd(Number.NaN)).toBe('—')
  })
})

describe('IMAGE_SIZE_MULTIPLIER', () => {
  it('matches backend getDefaultImagePrice multipliers', () => {
    expect(IMAGE_SIZE_MULTIPLIER).toEqual({ '1K': 1, '2K': 1.5, '4K': 2 })
  })
})
