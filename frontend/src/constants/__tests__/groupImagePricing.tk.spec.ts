import { describe, expect, it } from 'vitest'
import { GROUP_IMAGE_PRICING_PLATFORMS, supportsGroupImagePricing } from '../groupImagePricing.tk'

describe('groupImagePricing.tk', () => {
  it('includes newapi alongside legacy image platforms', () => {
    expect(GROUP_IMAGE_PRICING_PLATFORMS).toContain('newapi')
    expect(GROUP_IMAGE_PRICING_PLATFORMS).toEqual(
      expect.arrayContaining(['openai', 'gemini', 'antigravity', 'newapi'])
    )
  })

  it('supportsGroupImagePricing is case-insensitive', () => {
    expect(supportsGroupImagePricing('newapi')).toBe(true)
    expect(supportsGroupImagePricing('NEWAPI')).toBe(true)
    expect(supportsGroupImagePricing('anthropic')).toBe(false)
    expect(supportsGroupImagePricing('')).toBe(false)
  })
})
