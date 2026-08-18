/**
 * TokenKey: per-user pricing catalog API client.
 *
 * Backed by GET /api/v1/me/pricing-catalog
 * (handler/me_pricing_catalog_handler_tk.go). Returns the model menu for
 * the group of a direct API key, or the accessible-group union for an
 * automatic-routing key, at OFFICIAL list prices —
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
  /** Higher output price charged in thinking mode for the same model id, copied
   *  from the public catalog. Present only when the model has a thinking-mode
   *  premium; `output_per_1k` stays the non-thinking rate. */
  thinking_output_per_1k?: number
  /** Context-length interval (阶梯) ladder, copied verbatim from the public catalog
   *  (single source of truth — me-pricing is the official list price). The flat
   *  input/output fields carry the first tier. Absent for flat-priced models. */
  tiers?: MePricingTier[]
  /** Video resolution×audio ladder, copied verbatim from the public catalog. */
  video_price_tiers?: MePricingVideoTier[]
  /** Time-of-day (峰谷) pricing, copied verbatim from the public catalog. The flat
   *  fields above are the off-peak (谷时) price; these are the peak side. */
  peak_valley?: MePricingPeakValley
}

/** Peak-window list price for models with an upstream time-of-day multiplier.
 *  Mirrors PublicPricingPeakValley — see api/pricing.ts. */
export interface MePricingPeakValley {
  timezone: string
  windows: string[]
  peak_multiplier: number
  /** Peak-side prices. Always present (backend marshals these without omitempty). */
  input_per_1k: number
  output_per_1k: number
  cache_read_per_1k?: number
}

/** One context-length bracket of a tiered (阶梯) price. Matching is left-open,
 *  right-closed `(min_tokens, max_tokens]` — see FindMatchingInterval in
 *  backend/internal/service/channel.go. `max_tokens` absent = open-ended top
 *  tier. Per 1k tokens. */
export interface MePricingTier {
  min_tokens: number
  max_tokens?: number
  input_per_1k?: number
  output_per_1k?: number
  cache_read_per_1k?: number
}

/** One video resolution (and optional silent / image-input) bracket. USD/s. */
export interface MePricingVideoTier {
  resolution: string
  per_second: number
  per_second_silent?: number
  input_image_surcharge_per_second?: number
  default_for_model?: boolean
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
  /** Accessible groups that can serve this model — "授权分组" column when logged in. */
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
  group_id: number | null
  group_name?: string
  routing_mode: 'direct' | 'universal'
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
  /** Null when apiKeyId selects an automatic-routing key with user-wide scope. */
  target_group: MePricingTargetGroup | null
  models: MePricingModel[]
  my_keys: MePricingKeyRef[]
  accessible_groups: MePricingGroupRef[]
  /** Full model_id → authorized-groups index for the authenticated public catalog. */
  authorized_groups_by_model?: Record<string, MePricingModelGroup[]>
  updated_at: string
}

export interface GetMePricingCatalogParams {
  apiKeyId?: number
  groupId?: number
}

/**
 * Fetch the per-user pricing catalog.
 *
 * - With neither param: defaults to the user's first usable active key scope.
 * - With `apiKeyId`: shows a direct key's group menu or an automatic key's
 *   user-level authorized union.
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
