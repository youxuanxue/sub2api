import { describe, expect, it } from 'vitest'
import ExcelJS from 'exceljs'
import { fillHistoricalBillingWorkbook, historicalBillingCSV } from '../historicalBillingExport'
import type { HistoricalBillingReport, HistoricalBillingRow } from '@/api/historicalBilling'
const row: HistoricalBillingRow = { date: '2026-06-04', platform: ' =SUM(1,2)', requests: 3, input_tokens: 10, output_tokens: 20, cache_creation_tokens: 30, cache_read_tokens: 40, total_tokens: 100, actual_cost: '123456.1234567891', computed_at: '2026-10-29T00:00:00Z', provisional: false }
const report: HistoricalBillingReport = { user_id: 16, registered_at: '2026-06-04T00:00:00Z', start_date: '2026-06-04', end_date: '2026-10-29', timezone: 'UTC', currency: 'USD', granularity: 'day', platform: null, platforms: [row.platform], available_start_date: '2026-06-04', available_end_date: '2026-06-04', generated_at: '2026-10-29T01:00:00Z', rows: [row], totals: row }
const labels = { headers: Array.from({ length: 20 }, (_, i) => `column-${i}`), total: 'Total', provisional: 'In progress', recorded: 'Recorded', unknownPlatform: 'Unspecified' }

describe('historical bill downloads', () => {
  it('preserves monetary precision, server totals, scope and formula-safe dimensions in CSV', () => {
    const csv = historicalBillingCSV(report, labels)
    expect(csv).toContain('"\' =SUM(1,2)"')
    expect(csv).toContain('"123456.1234567891"')
    expect(csv).toContain('"UTC"')
    expect(csv).toContain('"2026-06-04","2026-10-29","day","*"')
    expect(csv.split('\r\n')[2]).toContain('"Total","","3","10","20","30","40","100","123456.1234567891"')
  })
  it('round-trips readable Excel cells without executing platform formulas or rounding money', async () => {
    const wb = new ExcelJS.Workbook()
    fillHistoricalBillingWorkbook(wb, report, labels)
    const read = new ExcelJS.Workbook()
    await read.xlsx.load(await wb.xlsx.writeBuffer())
    const sheet = read.getWorksheet('Billing')!
    expect(sheet.getCell('C2').value).toBe(' =SUM(1,2)')
    expect(sheet.getCell('C2').type).toBe(ExcelJS.ValueType.String)
    expect(sheet.getCell('J2').value).toBe('123456.1234567891')
    expect(sheet.getCell('J3').value).toBe(report.totals.actual_cost)
    expect(sheet.getCell('I2').value).toBe(100)
    expect(sheet.getCell('L2').value).toBe('UTC')
    expect(sheet.getCell('S2').value).toBe('2026-06-04')
  })
})
