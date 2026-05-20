/**
 * TokenKey: per-user pricing catalog API client.
 *
 * Backed by GET /api/v1/me/pricing-catalog
 * (handler/me_pricing_catalog_handler_tk.go). Returns the model menu for
 * the group of the user's selected API key, with prices already multiplied
 * by the user's effective rate (group.rate_multiplier × per-user override).
 * The same endpoint serves the "explore other group" comparison view when
 * `group_id` is supplied.
 *
 * Response shape uses the standard `{code,message,data}` envelope, which
 * the axios response interceptor unwraps — callers receive the `data`
 * payload directly.
 */

import { apiClient } from './client'

/** Billing mode mirrors backend service.BillingMode. */
export type MePricingBillingMode = 'token' | 'per_request' | 'image' | string

/**
 * "Your price" — already multiplied by effective rate (group default ×
 * per-user override). Per-1k for token modes; per-call for per_request.
 * Nil-valued fields mean "no data" (the field is omitted from the JSON
 * payload entirely thanks to backend `omitempty`).
 */
export interface MePricingPrice {
  currency: string
  input_per_1k?: number
  output_per_1k?: number
  cache_read_per_1k?: number
  cache_write_per_1k?: number
  image_output_per_1k?: number
  per_request?: number
}

export interface MePricingModel {
  model_id: string
  vendor?: string
  billing_mode: MePricingBillingMode
  your_price: MePricingPrice
  context_window?: number
  max_output_tokens?: number
  capabilities: string[]
}

export interface MePricingTargetGroup {
  id: number
  name: string
  platform: string
  /** Effective multiplier (group default × per-user override). */
  rate_multiplier: number
  /** Group default multiplier — only used to show "含个人覆写" hint. */
  list_multiplier: number
  has_override: boolean
  is_exclusive: boolean
  subscription_type: string
}

export interface MePricingKeyRef {
  id: number
  name: string
  group_id: number
  group_name: string
}

export interface MePricingGroupRef {
  id: number
  name: string
  platform: string
  rate_multiplier: number
  is_current_for_key: boolean
  is_exclusive: boolean
  subscription_type: string
}

export interface MePricingCatalogResponse {
  target_group: MePricingTargetGroup
  models: MePricingModel[]
  my_keys: MePricingKeyRef[]
  accessible_groups: MePricingGroupRef[]
  updated_at: string
}

export interface GetMePricingCatalogParams {
  apiKeyId?: number
  groupId?: number
}

/**
 * Fetch the per-user pricing catalog.
 *
 * - With neither param: defaults to the user's first active key's group.
 * - With `apiKeyId`: shows the menu for that key's group.
 * - With `groupId`: "explore other group" mode (must be in user's
 *   accessible set, otherwise 403).
 * - With BOTH and they refer to different groups: 400 (the API client
 *   surfaces this as a rejected promise; callers should set one at a time).
 */
export async function getMePricingCatalog(
  params: GetMePricingCatalogParams = {}
): Promise<MePricingCatalogResponse> {
  const query: Record<string, string> = {}
  if (params.apiKeyId != null) query.api_key_id = String(params.apiKeyId)
  if (params.groupId != null) query.group_id = String(params.groupId)
  const { data } = await apiClient.get<MePricingCatalogResponse>('/me/pricing-catalog', {
    params: query,
  })
  return data
}
