import { describe, expect, it } from 'vitest'
import type { PublicCatalogModel } from '@/api/pricing'
import { filterPricingCatalogByModel } from '../pricingCatalogSearch'

function model(id: string): PublicCatalogModel {
  return {
    model_id: id,
    pricing: {
      currency: 'USD',
      input_per_1k_tokens: 0,
      output_per_1k_tokens: 0
    },
    capabilities: []
  }
}

describe('filterPricingCatalogByModel', () => {
  const rows = [model('claude-sonnet-4'), model('gpt-4o-mini'), model('GPT-4-Turbo')]

  it('returns all rows when query is empty or whitespace', () => {
    expect(filterPricingCatalogByModel(rows, '', 'fuzzy')).toEqual(rows)
    expect(filterPricingCatalogByModel(rows, '   ', 'exact')).toEqual(rows)
  })

  it('fuzzy mode matches case-insensitive substring', () => {
    expect(filterPricingCatalogByModel(rows, 'sonnet', 'fuzzy').map((m) => m.model_id)).toEqual([
      'claude-sonnet-4'
    ])
    expect(filterPricingCatalogByModel(rows, 'gpt', 'fuzzy').map((m) => m.model_id)).toEqual([
      'gpt-4o-mini',
      'GPT-4-Turbo'
    ])
  })

  it('exact mode matches case-insensitive full model_id', () => {
    expect(filterPricingCatalogByModel(rows, 'gpt-4o-mini', 'exact').map((m) => m.model_id)).toEqual([
      'gpt-4o-mini'
    ])
    expect(filterPricingCatalogByModel(rows, 'GPT-4-TURBO', 'exact').map((m) => m.model_id)).toEqual([
      'GPT-4-Turbo'
    ])
  })

  it('exact mode does not use substring semantics', () => {
    expect(filterPricingCatalogByModel(rows, 'gpt', 'exact')).toEqual([])
  })
})
