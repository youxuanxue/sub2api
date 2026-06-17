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
  /** Higher output price charged in thinking mode for the same model id
   *  (Alibaba DashScope qwen3 dense). Present only when the model has a
   *  thinking-mode premium; output_per_1k_tokens stays the non-thinking rate. */
  thinking_output_per_1k_tokens?: number
  cache_read_per_1k?: number
  cache_write_per_1k?: number
  /** "token" (default/omitted), "image" (per-image) or "video" (per-second). */
  billing_mode?: string
  /** USD per generated image (image billing_mode). */
  output_cost_per_image?: number
  /** USD per second of generated video (video billing_mode). */
  output_cost_per_second?: number
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
