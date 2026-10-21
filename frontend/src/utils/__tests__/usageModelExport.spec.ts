import { describe, expect, it } from 'vitest'
import ExcelJS from 'exceljs'
import type { ModelStat } from '@/types'
import { fillModelUsageWorkbook } from '../usageModelExport'

const summary = (values: Partial<ModelStat> = {}): ModelStat => ({ model: 'model-a', input_tokens: 200, output_tokens: 40, cache_read_tokens: 60, cache_creation_tokens: 100, total_tokens: 400, requests: 2, actual_cost: 0.3, cost: 99, account_cost: 88, ...values })
const labels = { title: '模型汇总', scope: '#16 · 2026-10-20', note: 'Actual charges', rate: 'USD to CNY', total: '合计', headers: ['序号', '模型', 'Token', '均价', '小计', '请求', 'USD'] }

describe('model usage customer workbook', () => {
  it('uses backend totals verbatim with numeric formulas and no internal costs', async () => {
    const wb = new ExcelJS.Workbook()
    fillModelUsageWorkbook(wb, [summary(), summary({ model: '=SUM(1,2)', total_tokens: 0, requests: 1, actual_cost: 0.05 })], 7, labels)
    const read = new ExcelJS.Workbook()
    await read.xlsx.load(await wb.xlsx.writeBuffer())
    const ws = read.worksheets[0]
    expect(ws.getCell('B7').value).toBe('model-a')
    expect(ws.getCell('C7').value).toBe(400)
    expect(ws.getCell('F7').value).toBe(2)
    expect(ws.getCell('G7').value).toBeCloseTo(0.3)
    expect(ws.getCell('E7').formula).toBe('G7*$D$3')
    expect(ws.getCell('E7').result).toBeCloseTo(2.1)
    expect(ws.getCell('D7').formula).toBe('E7/C7')
    expect(ws.getCell('D7').result).toBeCloseTo(0.00525)
    expect(ws.getCell('B8').value).toBe('=SUM(1,2)')
    expect(ws.getCell('B8').type).toBe(ExcelJS.ValueType.String)
    expect(ws.getCell('D8').value).toBeNull()
    expect(ws.getCell('E9').result).toBeCloseTo(2.45)
    expect(ws.getCell('G9').result).toBeCloseTo(0.35)
    expect(ws.columnCount).toBe(7)
  })
  it('rejects incomplete charges, empty data and invalid exchange rates', () => {
    expect(() => fillModelUsageWorkbook(new ExcelJS.Workbook(), [summary({ actual_cost: NaN })], 7, labels)).toThrow('Invalid usage')
    expect(() => fillModelUsageWorkbook(new ExcelJS.Workbook(), [], 7, labels)).toThrow('No usage')
    for (const rate of [0, -1, NaN, Infinity]) {
      expect(() => fillModelUsageWorkbook(new ExcelJS.Workbook(), [summary()], rate, labels)).toThrow('Invalid exchange')
    }
  })
})
