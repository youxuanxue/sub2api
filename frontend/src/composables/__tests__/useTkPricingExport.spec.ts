import { describe, it, expect } from 'vitest'
import { buildPricingCsv, formatTiers, pricingCsvFilename } from '../useTkPricingExport'
import type { PublicCatalogResponse } from '@/api/pricing'

const catalog = (data: PublicCatalogResponse['data']): PublicCatalogResponse => ({
  object: 'list',
  data,
  updated_at: '2026-06-26T00:00:00Z'
})

/**
 * Split one CSV record into cells, honouring RFC-4180 quoting so a quoted cell
 * containing commas (the tiers ladder, the peak-window list) counts as one cell.
 * Needed to assert column alignment — a naive split(',') would report a shifted
 * row as correct.
 */
function parseCsvRow(row: string): string[] {
  const cells: string[] = []
  let cell = ''
  let quoted = false
  for (let i = 0; i < row.length; i++) {
    const ch = row[i]
    if (quoted) {
      if (ch === '"') {
        if (row[i + 1] === '"') {
          cell += '"'
          i++
        } else {
          quoted = false
        }
      } else {
        cell += ch
      }
    } else if (ch === '"') {
      quoted = true
    } else if (ch === ',') {
      cells.push(cell)
      cell = ''
    } else {
      cell += ch
    }
  }
  cells.push(cell)
  return cells
}

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
    // the ladder is one cell, segments joined by ' | '. Brackets are written
    // (min, max] to match FindMatchingInterval's left-open/right-closed billing.
    expect(csv).toContain('(0, 128k]: in 0.119 / out 0.298 | (128k, ∞): in 0.716 / out 7.164')
  })

  it('carries each tier\'s cache-read price into the ladder cell', () => {
    const csv = buildPricingCsv(
      catalog([
        {
          model_id: 'glm-4.7',
          vendor: 'zhipu',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0.0004478,
            output_per_1k_tokens: 0.0020896,
            tiers: [
              {
                min_tokens: 0,
                max_tokens: 32000,
                input_per_1k_tokens: 0.0004478,
                output_per_1k_tokens: 0.0020896,
                cache_read_per_1k: 0.0000896
              },
              {
                min_tokens: 32000,
                input_per_1k_tokens: 0.000597,
                output_per_1k_tokens: 0.0023881,
                cache_read_per_1k: 0.0001194
              }
            ]
          },
          capabilities: []
        }
      ])
    )
    expect(csv).toContain('(0, 32k]: in 0.448 / out 2.09 / cache 0.09')
    expect(csv).toContain('(32k, ∞): in 0.597 / out 2.388 / cache 0.119')
  })

  it('emits the peak/valley columns (flat fields stay the off-peak price)', () => {
    const csv = buildPricingCsv(
      catalog([
        {
          model_id: 'deepseek-v4-pro',
          vendor: 'deepseek',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0.000435,
            output_per_1k_tokens: 0.00087,
            peak_valley: {
              timezone: 'Asia/Shanghai',
              windows: ['09:00-12:00', '14:00-18:00'],
              peak_multiplier: 2,
              input_per_1k_tokens: 0.00087,
              output_per_1k_tokens: 0.00174
            }
          },
          capabilities: []
        }
      ])
    )
    const [header, row] = csv.split('\r\n')
    const cells = header.split(',')
    // windows share a cell, separated by ';' so the cell stays one CSV column
    expect(row).toContain('09:00-12:00; 14:00-18:00')
    expect(row).toContain('Asia/Shanghai')
    // peak = flat × multiplier: 0.435/1M off-peak → 0.87/1M peak
    const values = parseCsvRow(row)
    expect(values[cells.indexOf('input_per_1M')]).toBe('0.435')
    expect(values[cells.indexOf('peak_input_per_1M')]).toBe('0.87')
    expect(values[cells.indexOf('peak_output_per_1M')]).toBe('1.74')
    expect(values[cells.indexOf('peak_multiplier')]).toBe('2')
  })

  it('keeps every row aligned with the header (no shifted columns)', () => {
    const csv = buildPricingCsv(
      catalog([
        // flat, tiered and peak-priced rows must all emit the same cell count —
        // a value added to the header without a matching cell silently shifts
        // every later column in the sheet.
        { model_id: 'flat', vendor: 'openai', pricing: { currency: 'USD', input_per_1k_tokens: 0.001, output_per_1k_tokens: 0.002 }, capabilities: [] },
        {
          model_id: 'tiered',
          vendor: 'volcengine',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0.0004,
            output_per_1k_tokens: 0.002,
            tiers: [
              { min_tokens: 0, max_tokens: 32000, input_per_1k_tokens: 0.0004, output_per_1k_tokens: 0.002 },
              { min_tokens: 32000, input_per_1k_tokens: 0.0007, output_per_1k_tokens: 0.0035 }
            ]
          },
          capabilities: []
        },
        {
          model_id: 'peak',
          vendor: 'deepseek',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0.0004,
            output_per_1k_tokens: 0.0008,
            peak_valley: {
              timezone: 'Asia/Shanghai',
              windows: ['09:00-12:00'],
              peak_multiplier: 2,
              input_per_1k_tokens: 0.0008,
              output_per_1k_tokens: 0.0016,
              cache_read_per_1k: 0.00001
            }
          },
          capabilities: ['vision']
        }
      ])
    )
    const rows = csv.split('\r\n')
    const expected = rows[0].split(',').length
    for (const row of rows.slice(1)) {
      expect(parseCsvRow(row)).toHaveLength(expected)
    }
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
