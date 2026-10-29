import type { Workbook } from 'exceljs'
import type { HistoricalBillingReport, HistoricalBillingRow } from '@/api/historicalBilling'
import { historicalBillingValues } from './historicalBillingColumns'

export interface HistoricalBillingExportLabels {
  headers: string[]
  total: string
  unknownPlatform: string
}

function exportRows(report: HistoricalBillingReport, labels: HistoricalBillingExportLabels) {
  const values = (row: HistoricalBillingRow, total = false) => {
    const cells = historicalBillingValues(row, labels.unknownPlatform)
    if (total) { cells[0] = labels.total; cells[1] = '' }
    return [report.user_id, ...cells, report.timezone]
  }
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
  sheet.columns.forEach((column, i) => { column.width = i === 2 ? 22 : 20 })
  sheet.getColumn(8).numFmt = '@' // exact decimal money; authoritative totals come from the server
  for (const index of [1, sheet.rowCount]) {
    sheet.getRow(index).font = { bold: true, color: { argb: 'FF0F766E' } }
    sheet.getRow(index).height = 32
    sheet.getRow(index).alignment = { vertical: 'middle', wrapText: true }
  }
  sheet.views = [{ state: 'frozen', ySplit: 1 }]
  sheet.autoFilter = { from: { row: 1, column: 1 }, to: { row: report.rows.length + 1, column: labels.headers.length } }
  return sheet
}
