/**
 * Public model + pricing catalog API client.
 *
 * Backed by GET /api/v1/public/pricing (handler/pricing_catalog_handler_tk.go).
 * The endpoint returns the OpenAI-compatible raw shape `{ object, data, updated_at }`
 * (no `{code,message,data}` envelope), so the axios response interceptor passes it through.
 *
 * docs/approved/user-cold-start.md §2 (v1 MVP).
 */

import { apiClient } from './client'

export interface PublicPricing {
  currency: string
  input_per_1k_tokens: number
  output_per_1k_tokens: number
  cache_read_per_1k?: number
  cache_write_per_1k?: number
}

export interface PublicCatalogModel {
  model_id: string
  vendor?: string
  pricing: PublicPricing
  context_window?: number
  max_output_tokens?: number
  capabilities: string[]
}

export interface PublicCatalogResponse {
  object: 'list'
  data: PublicCatalogModel[]
  updated_at: string
}

/**
 * Fetch the public pricing catalog. May return an empty `data[]` when the
 * upstream pricing source is unavailable; callers should render the empty
 * state rather than treating it as an error (US-028 AC-005).
 */
export async function getPublicPricing(): Promise<PublicCatalogResponse> {
  const { data } = await apiClient.get<PublicCatalogResponse>('/public/pricing')
  return data
}
