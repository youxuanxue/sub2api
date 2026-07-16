import { describe, expect, it } from 'vitest'
import {
  formatCatalogMediaPrice,
  formatCatalogPrice,
  pricingCatalogModality,
} from '../pricingCatalogPresentation.tk'

describe('pricingCatalogPresentation', () => {
  it('derives text, image, and video from the catalog billing mode', () => {
    expect(pricingCatalogModality('image')).toBe('image')
    expect(pricingCatalogModality('video')).toBe('video')
    expect(pricingCatalogModality('token')).toBe('text')
    expect(pricingCatalogModality(undefined)).toBe('text')
  })

  it('formats catalog prices with shared precision', () => {
    expect(formatCatalogPrice(0)).toBe('$0')
    expect(formatCatalogPrice(0.00125)).toBe('$0.001250')
    expect(formatCatalogPrice(0.03480597014925373)).toBe('$0.0348')
    expect(formatCatalogPrice(1.25)).toBe('$1.25')
    expect(formatCatalogPrice(Number.NaN)).toBe('—')
  })

  it('does not present missing or non-positive media prices as free', () => {
    expect(formatCatalogMediaPrice()).toBe('—')
    expect(formatCatalogMediaPrice(0)).toBe('—')
    expect(formatCatalogMediaPrice(-1)).toBe('—')
    expect(formatCatalogMediaPrice(0.6)).toBe('$0.6000')
  })
})
