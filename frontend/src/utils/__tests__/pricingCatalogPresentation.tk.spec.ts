import { describe, expect, it } from 'vitest'
import {
  catalogTokenPricePer1M,
  formatCatalogMediaPrice,
  formatCatalogPrice,
  formatCatalogTokenPrice,
  formatCatalogUsd,
  pricingCatalogModality,
} from '../pricingCatalogPresentation.tk'

describe('pricingCatalogPresentation', () => {
  it('derives text, image, and video from the catalog billing mode', () => {
    expect(pricingCatalogModality('image')).toBe('image')
    expect(pricingCatalogModality('video')).toBe('video')
    expect(pricingCatalogModality('token')).toBe('text')
    expect(pricingCatalogModality(undefined)).toBe('text')
  })

  it('formats all catalog USD amounts with at most 3 fractional digits', () => {
    expect(formatCatalogUsd(0)).toBe('$0')
    expect(formatCatalogUsd(0.006674)).toBe('$0.007')
    expect(formatCatalogUsd(0.03480597014925373)).toBe('$0.035')
    expect(formatCatalogUsd(0.6)).toBe('$0.6')
    expect(formatCatalogUsd(1.25)).toBe('$1.25')
    expect(formatCatalogUsd(Number.NaN)).toBe('—')
    expect(formatCatalogPrice(0.1264)).toBe('$0.126')
    expect(formatCatalogMediaPrice(0.6)).toBe('$0.6')
  })

  it('converts stored per-1K token prices to per-1M display', () => {
    expect(catalogTokenPricePer1M(0.000297)).toBeCloseTo(0.297, 6)
    expect(formatCatalogTokenPrice(0.000297)).toBe('$0.297')
    expect(formatCatalogTokenPrice(0.0001194)).toBe('$0.119')
    expect(formatCatalogTokenPrice(0.001844)).toBe('$1.844')
    expect(formatCatalogTokenPrice(0)).toBe('$0')
    expect(formatCatalogTokenPrice(Number.NaN)).toBe('—')
  })

  it('does not present missing or non-positive media prices as free', () => {
    expect(formatCatalogMediaPrice()).toBe('—')
    expect(formatCatalogMediaPrice(0)).toBe('—')
    expect(formatCatalogMediaPrice(-1)).toBe('—')
  })
})
