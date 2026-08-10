import { describe, expect, it } from 'vitest'
import {
  buildCatalogBillingIndex,
  priceMapFromMeCatalog,
  priceMapFromPublicCatalog,
} from '@/utils/studioMediaCatalog.tk'

describe('studioMediaCatalog', () => {
  it('buildCatalogBillingIndex maps billing_mode image and video only', () => {
    const idx = buildCatalogBillingIndex([
      { model_id: 'imagen-4.0-generate-001', pricing: { billing_mode: 'image' } },
      { model_id: 'grok-imagine-video', pricing: { billing_mode: 'video' } },
      { model_id: 'grok-4.3', pricing: { billing_mode: 'token' } },
      { model_id: 'bge-large-en', pricing: { billing_mode: 'embedding' } },
    ])
    expect(idx.get('imagen-4.0-generate-001')).toBe('image')
    expect(idx.get('grok-imagine-video')).toBe('video')
    expect(idx.has('grok-4.3')).toBe(false)
    expect(idx.has('bge-large-en')).toBe(false)
  })

  it('priceMapFromPublicCatalog carries billingMode and vendor', () => {
    const map = priceMapFromPublicCatalog(
      [
        {
          model_id: 'grok-imagine-video',
          vendor: 'xai',
          pricing: { billing_mode: 'video', output_cost_per_second: 0.08 },
        },
      ] as never,
      new Set(['grok-imagine-video'])
    )
    expect(map.get('grok-imagine-video')).toEqual({
      perSecond: 0.08,
      billingMode: 'video',
      vendor: 'xai',
    })
  })

  it('priceMapFromPublicCatalog ignores per-unit rows without media billing_mode', () => {
    const map = priceMapFromPublicCatalog(
      [
        {
          model_id: 'gemini-3.1-pro-low',
          vendor: 'antigravity',
          pricing: { output_cost_per_image: 0.00012 },
        },
        {
          model_id: 'broken-video',
          vendor: 'xai',
          pricing: { billing_mode: 'video', output_cost_per_image: 0.02 },
        },
      ] as never,
      new Set(['gemini-3.1-pro-low', 'broken-video'])
    )
    expect(map.size).toBe(0)
  })

  it('priceMapFromMeCatalog mirrors me pricing rows', () => {
    const map = priceMapFromMeCatalog([
      {
        model_id: 'grok-imagine-image',
        vendor: 'xai',
        billing_mode: 'image',
        your_price: { currency: 'USD', per_image: 0.02 },
        capabilities: [],
      },
    ] as never)
    expect(map.get('grok-imagine-image')).toEqual({
      perImage: 0.02,
      billingMode: 'image',
      vendor: 'xai',
    })
  })

  it('priceMapFromMeCatalog carries authenticated video tiers into Studio', () => {
    const map = priceMapFromMeCatalog([
      {
        model_id: 'doubao-seedance-2-0-260128',
        vendor: 'volcengine',
        billing_mode: 'video',
        your_price: {
          currency: 'USD',
          per_second: 0.06895880597014925,
          video_price_tiers: [
            { resolution: '480p', per_second: 0.06895880597014925 },
            { resolution: '1080p', per_second: 0.3699402985074627, default_for_model: true },
          ],
        },
        capabilities: [],
      },
    ] as never)

    expect(map.get('doubao-seedance-2-0-260128')?.videoTiers).toEqual([
      {
        resolution: '480p',
        perSecond: 0.06895880597014925,
        perSecondSilent: undefined,
        inputImageSurchargePerSecond: undefined,
        defaultForModel: undefined,
      },
      {
        resolution: '1080p',
        perSecond: 0.3699402985074627,
        perSecondSilent: undefined,
        inputImageSurchargePerSecond: undefined,
        defaultForModel: true,
      },
    ])
  })

  it('priceMapFromMeCatalog ignores per-unit rows without media billing_mode', () => {
    const map = priceMapFromMeCatalog([
      {
        model_id: 'gemini-3.1-pro-low',
        vendor: 'antigravity',
        billing_mode: 'token',
        your_price: { currency: 'USD', per_image: 0.00012 },
        capabilities: [],
      },
    ] as never)
    expect(map.size).toBe(0)
  })
})
