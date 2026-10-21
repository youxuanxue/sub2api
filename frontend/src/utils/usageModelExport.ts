import type { ModelStat } from '@/types'
import type { Workbook } from 'exceljs'

export type ModelUsageSummary = Pick<ModelStat, 'model' | 'total_tokens' | 'actual_cost' | 'requests'>

export interface ModelExportLabels {
  title: string; scope: string; note: string; rate: string; total: string; headers: string[]
}

export function fillModelUsageWorkbook(wb: Workbook, totals: ModelUsageSummary[], rate: number, labels: ModelExportLabels) {
  if (!totals.length) throw new Error('No usage to export')
  if (!Number.isFinite(rate) || rate <= 0) throw new Error('Invalid exchange rate')
  if (totals.some(item => [item.total_tokens, item.actual_cost, item.requests, item.actual_cost * rate]
    .some(value => typeof value !== 'number' || !Number.isFinite(value) || value < 0))) {
    throw new Error('Invalid usage values')
  }
  const ws = wb.addWorksheet(labels.title)
  ws.columns = [{ width: 9 }, { width: 38 }, { width: 22 }, { width: 25 }, { width: 22 }, { width: 15 }, { width: 22 }]
  ws.mergeCells('A1:G1'); ws.getCell('A1').value = labels.title
  ws.getCell('A1').font = { size: 18, bold: true, color: { argb: 'FF0F766E' } }
  ws.mergeCells('A2:G2'); ws.getCell('A2').value = labels.scope
  ws.getRow(2).height = 45; ws.getCell('A2').alignment = { wrapText: true, vertical: 'middle' }
  ws.getCell('A3').value = labels.rate; ws.mergeCells('A3:C3'); ws.getCell('D3').value = rate
  ws.getCell('D3').numFmt = '0.000000'; ws.getCell('D3').font = { color: { argb: 'FF2563EB' } }
  ws.mergeCells('A4:G4'); ws.getCell('A4').value = labels.note
  ws.getCell('A4').alignment = { wrapText: true, vertical: 'middle' }; ws.getRow(4).height = 45
  ws.getRow(6).values = labels.headers
  const rows = [...totals].sort((a, b) => b.actual_cost - a.actual_cost || a.model.localeCompare(b.model))
  rows.forEach((item, index) => {
    const row = index + 7
    ws.getRow(row).values = [index + 1, item.model, item.total_tokens,
      item.total_tokens > 0 ? { formula: `E${row}/C${row}`, result: item.actual_cost * rate / item.total_tokens } : null,
      { formula: `G${row}*$D$3`, result: item.actual_cost * rate }, item.requests, item.actual_cost]
    ws.getCell(`C${row}`).numFmt = '#,##0'; ws.getCell(`D${row}`).numFmt = '0.0000000000'
    ws.getCell(`E${row}`).numFmt = '#,##0.000000'; ws.getCell(`F${row}`).numFmt = '#,##0'
    ws.getCell(`G${row}`).numFmt = '#,##0.000000'
  })
  const last = rows.length + 6
  const sumRow = last + 1
  ws.getCell(`B${sumRow}`).value = labels.total
  for (const [col, key] of [['C', 'total_tokens'], ['F', 'requests'], ['G', 'actual_cost']] as const) {
    ws.getCell(`${col}${sumRow}`).value = { formula: `SUM(${col}7:${col}${last})`, result: rows.reduce((sum, item) => sum + item[key], 0) }
    ws.getCell(`${col}${sumRow}`).numFmt = col === 'G' ? '#,##0.000000' : '#,##0'
  }
  ws.getCell(`E${sumRow}`).value = { formula: `SUM(E7:E${last})`, result: rows.reduce((sum, item) => sum + item.actual_cost * rate, 0) }
  ws.getCell(`E${sumRow}`).numFmt = '#,##0.000000'
  for (const rowNumber of [6, sumRow]) {
    const row = ws.getRow(rowNumber)
    row.height = 30
    row.eachCell(cell => {
      cell.font = { bold: true, color: { argb: rowNumber === 6 ? 'FFFFFFFF' : 'FF0F766E' } }
      cell.fill = { type: 'pattern', pattern: 'solid', fgColor: { argb: rowNumber === 6 ? 'FF0F766E' : 'FFE6F5F2' } }
      cell.alignment = { vertical: 'middle', wrapText: true }
    })
  }
  ws.views = [{ state: 'frozen', ySplit: 6 }]
  ws.autoFilter = `A6:G${last}`
  ws.pageSetup = { orientation: 'landscape', paperSize: 9, fitToPage: true, fitToWidth: 1, fitToHeight: 0, printTitlesRow: '1:6' }
  wb.calcProperties.fullCalcOnLoad = true
  return ws
}
