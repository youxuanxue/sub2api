import { apiClient } from './client'

export interface HistoricalBillingRow {
  date: string
  platform: string
  requests: number
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  total_tokens: number
  actual_cost: string
  computed_at: string
  provisional: boolean
}
export interface HistoricalBillingReport {
  user_id: number
  registered_at: string
  start_date: string
  end_date: string
  timezone: string
  currency: 'USD'
  granularity: 'day' | 'month'
  platform: string | null
  platforms: string[]
  available_start_date: string
  available_end_date: string
  generated_at: string
  rows: HistoricalBillingRow[]
  totals: HistoricalBillingRow
}
export interface HistoricalBillingParams {
  start_date?: string
  end_date?: string
  granularity: 'day' | 'month'
  platform?: string
}
export async function getHistoricalBilling(params: HistoricalBillingParams, options: {
  admin: boolean; userId?: number; signal: AbortSignal
}): Promise<HistoricalBillingReport> {
  const { data } = await apiClient.get<HistoricalBillingReport>(
    options.admin ? '/admin/usage/billing-history' : '/usage/billing-history',
    { params: { ...params, ...(options.admin ? { user_id: options.userId } : {}) }, signal: options.signal }
  )
  return data
}
