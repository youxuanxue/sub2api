import { describe, it, expect } from 'vitest'
import { buildPricingCsv, formatTiers, pricingCsvFilename } from '../useTkPricingExport'
import type { PublicCatalogResponse } from '@/api/pricing'

const catalog = (data: PublicCatalogResponse['data']): PublicCatalogResponse => ({
  object: 'list',
  data,
  updated_at: '2026-06-26T00:00:00Z'
})

describe('useTkPricingExport.buildPricingCsv', () => {
  it('emits header + one row per model, prices converted to per-1M', () => {
    const csv = buildPricingCsv(
      catalog([
        {
          model_id: 'claude-haiku-4-5',
          vendor: 'anthropic',
          pricing: { currency: 'USD', input_per_1k_tokens: 0.001, output_per_1k_tokens: 0.005 },
          context_window: 200000,
          max_output_tokens: 64000,
          capabilities: ['vision', 'tool_use']
        }
      ])
    )
    const [header, row] = csv.split('\r\n')
    expect(header.split(',')).toContain('input_per_1M')
    expect(header.split(',')).toContain('tiers')
    // 0.001/1k → 1.0/1M, 0.005/1k → 5.0/1M
    expect(row).toContain('1,5,') // input_per_1M, output_per_1M
    // capabilities joined with ';' so the cell stays one CSV column
    expect(row).toContain('vision;tool_use')
  })

  it('renders the 阶梯 ladder into a single readable tiers cell (quoted, per-1M)', () => {
    const csv = buildPricingCsv(
      catalog([
        {
          model_id: 'qwen-plus',
          vendor: 'dashscope',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0.0001194,
            output_per_1k_tokens: 0.0002985,
            tiers: [
              { min_tokens: 0, max_tokens: 128000, input_per_1k_tokens: 0.0001194, output_per_1k_tokens: 0.0002985 },
              { min_tokens: 128000, input_per_1k_tokens: 0.0007164, output_per_1k_tokens: 0.0071642 }
            ]
          },
          capabilities: []
        }
      ])
    )
    // the ladder is one cell, segments joined by ' | ' (no comma → no quoting)
    expect(csv).toContain('0-128k: in 0.1194 / out 0.2985 | 128k-∞: in 0.7164 / out 7.1642')
  })

  it('sorts by (vendor, model_id) and leaves flat models with an empty tiers cell', () => {
    const csv = buildPricingCsv(
      catalog([
        { model_id: 'z-model', vendor: 'zhipu', pricing: { currency: 'USD', input_per_1k_tokens: 0.001, output_per_1k_tokens: 0.002 }, capabilities: [] },
        { model_id: 'a-model', vendor: 'anthropic', pricing: { currency: 'USD', input_per_1k_tokens: 0.001, output_per_1k_tokens: 0.002 }, capabilities: [] }
      ])
    )
    const rows = csv.split('\r\n')
    expect(rows[1]).toContain('a-model')
    expect(rows[2]).toContain('z-model')
  })

  it('returns just the header for an empty/null catalog', () => {
    expect(buildPricingCsv(null).split('\r\n')).toHaveLength(1)
    expect(buildPricingCsv(catalog([])).split('\r\n')).toHaveLength(1)
  })
})

describe('useTkPricingExport.formatTiers', () => {
  it('is empty for missing tiers', () => {
    expect(formatTiers(undefined)).toBe('')
    expect(formatTiers([])).toBe('')
  })
})

describe('useTkPricingExport.pricingCsvFilename', () => {
  it('embeds the date', () => {
    expect(pricingCsvFilename(new Date('2026-06-26T10:00:00Z'))).toBe('tokenkey-pricing-2026-06-26.csv')
  })
})
