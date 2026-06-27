/**
 * TokenKey: per-user pricing catalog API client.
 *
 * Backed by GET /api/v1/me/pricing-catalog
 * (handler/me_pricing_catalog_handler_tk.go). Returns the model menu for
 * the group of the user's selected API key, at OFFICIAL list prices —
 * decoupled from the group/override rate (TK pricing-display policy; see
 * me_pricing_catalog_tk.go header). The same endpoint serves the "explore
 * other group" comparison view when `group_id` is supplied.
 *
 * Response shape uses the standard `{code,message,data}` envelope, which
 * the axios response interceptor unwraps — callers receive the `data`
 * payload directly.
 */

import { apiClient } from './client'

/** Billing mode mirrors backend service.BillingMode (+ 'video' for per-second media). */
export type MePricingBillingMode = 'token' | 'per_request' | 'image' | 'video' | string

/**
 * Official list price (field name kept as `your_price` for DTO stability,
 * but no longer multiplied by the group/override rate). Per-1k for token
 * modes; per-call for per_request. Nil-valued fields mean "no data" (the
 * field is omitted from the JSON payload entirely thanks to backend
 * `omitempty`).
 */
export interface MePricingPrice {
  currency: string
  input_per_1k?: number
  output_per_1k?: number
  cache_read_per_1k?: number
  cache_write_per_1k?: number
  image_output_per_1k?: number
  per_request?: number
  /** USD per generated image (image billing_mode), scaled by the user's rate. */
  per_image?: number
  /** USD per second of generated video (video billing_mode), scaled by the user's rate. */
  per_second?: number
  /** Input-token interval (阶梯) ladder, copied verbatim from the public catalog
   *  (single source of truth — me-pricing is the official list price). The flat
   *  input/output fields carry the first tier. Absent for flat-priced models. */
  tiers?: MePricingTier[]
}

/** One input-token bracket of a tiered (阶梯) price. `min_tokens` inclusive,
 *  `max_tokens` exclusive; `max_tokens` absent = open-ended top tier. Per 1k tokens. */
export interface MePricingTier {
  min_tokens: number
  max_tokens?: number
  input_per_1k?: number
  output_per_1k?: number
  cache_read_per_1k?: number
}

/** One accessible group that can serve a given model — the "授权分组" column.
 *  Trimmed group ref (no per-key flags) for the per-model badge + create-key
 *  deep-link. Absent on the public catalog. */
export interface MePricingModelGroup {
  id: number
  name: string
  platform: string
  is_exclusive: boolean
  is_current_for_key: boolean
  rate_multiplier: number
  subscription_type?: string
}

export interface MePricingModel {
  model_id: string
  vendor?: string
  billing_mode: MePricingBillingMode
  your_price: MePricingPrice
  context_window?: number
  max_output_tokens?: number
  capabilities: string[]
  /** Accessible groups (exclusive + public) that can serve this model.
   *  Only present on the authenticated "my" view; empty/omitted publicly. */
  authorized_groups?: MePricingModelGroup[]
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
