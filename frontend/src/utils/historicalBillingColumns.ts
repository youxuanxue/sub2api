import type { HistoricalBillingRow } from '@/api/historicalBilling'

// Shared order and fields for the on-screen bill and both download formats.
export const HISTORICAL_BILLING_COLUMNS = [
  { key: 'date', header: 'date' },
  { key: 'platform', header: 'platform' },
  { key: 'requests', header: 'requests' },
  { key: 'input_tokens', header: 'input' },
  { key: 'output_tokens', header: 'output' },
  { key: 'total_tokens', header: 'tokens' },
  { key: 'actual_cost', header: 'cost' },
] as const satisfies ReadonlyArray<{ key: keyof HistoricalBillingRow; header: string }>

export function historicalBillingValues(row: HistoricalBillingRow, unknownPlatform: string) {
  return HISTORICAL_BILLING_COLUMNS.map(({ key }) => key === 'platform' ? row.platform || unknownPlatform : row[key])
}
