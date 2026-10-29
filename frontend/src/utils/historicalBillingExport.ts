import type { Workbook } from 'exceljs'
import type { HistoricalBillingReport, HistoricalBillingRow } from '@/api/historicalBilling'

export interface HistoricalBillingExportLabels {
  headers: string[]
  total: string
  provisional: string
  recorded: string
  unknownPlatform: string
}

function exportRows(report: HistoricalBillingReport, labels: HistoricalBillingExportLabels) {
  const values = (row: HistoricalBillingRow, total = false) => [
    report.user_id, total ? labels.total : row.date,
    total ? '' : row.platform || labels.unknownPlatform,
    row.requests, row.input_tokens, row.output_tokens, row.cache_creation_tokens,
    row.cache_read_tokens, row.total_tokens, row.actual_cost,
    row.provisional ? labels.provisional : labels.recorded,
    report.timezone, row.computed_at, report.generated_at,
    report.start_date, report.end_date, report.granularity,
    report.platform === null ? '*' : report.platform || labels.unknownPlatform,
    report.available_start_date, report.available_end_date,
  ]
  return [labels.headers, ...report.rows.map(row => values(row)), values(report.totals, true)]
}

function csvCell(value: string | number): string {
  let text = String(value)
  // Text dimensions can come from persisted provider identifiers. Neutralize
  // formula prefixes including those hidden behind whitespace/control bytes.
  if (typeof value === 'string' && /^[\s]*[=+\-@]/.test(text)) text = `'${text}`
  return `"${text.replace(/"/g, '""')}"`
}

export function historicalBillingCSV(report: HistoricalBillingReport, labels: HistoricalBillingExportLabels): string {
  return '\uFEFF' + exportRows(report, labels).map(row => row.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

export function fillHistoricalBillingWorkbook(wb: Workbook, report: HistoricalBillingReport, labels: HistoricalBillingExportLabels) {
  const sheet = wb.addWorksheet('Billing')
  for (const row of exportRows(report, labels)) sheet.addRow(row)
  sheet.columns.forEach((column, i) => { column.width = i >= 11 ? 26 : i === 2 ? 22 : 20 })
  sheet.getColumn(10).numFmt = '@' // exact decimal money; authoritative totals come from the server
  for (const index of [1, sheet.rowCount]) {
    sheet.getRow(index).font = { bold: true, color: { argb: 'FF0F766E' } }
    sheet.getRow(index).height = 32
    sheet.getRow(index).alignment = { vertical: 'middle', wrapText: true }
  }
  sheet.views = [{ state: 'frozen', ySplit: 1 }]
  sheet.autoFilter = { from: { row: 1, column: 1 }, to: { row: report.rows.length + 1, column: labels.headers.length } }
  return sheet
}
