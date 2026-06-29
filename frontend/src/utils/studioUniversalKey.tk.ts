/**
 * TokenKey-only: helpers for Universal (全能) API keys in the Studio.
 *
 * Per-request routing (video/image/chat) works on universal keys — the gap is
 * Studio discovery: GET /v1/models skips universal resolution (metadata endpoint)
 * and me/pricing-catalog rejects api_key_id when group_id IS NULL.
 */
import type { ApiKey } from '@/types'
import type { MePricingCatalogResponse } from '@/api/me-pricing'
import type { PublicCatalogModel } from '@/api/pricing'
import type { MediaPrice, MediaPriceMap } from '@/constants/mediaTiers.tk'

export function isUniversalKey(k: ApiKey | undefined | null): boolean {
  if (!k) return false
  if (k.routing_mode === 'universal') return true
  return k.group_id == null && k.group == null
}

/** Model ids the user may reach across ALL accessible groups (pricing catalog index). */
export function entitledModelIds(catalog: MePricingCatalogResponse | null): Set<string> {
  const idx = catalog?.authorized_groups_by_model
  if (!idx) return new Set()
  return new Set(Object.keys(idx))
}

/** Official media prices for entitled models (public catalog ∩ authorized index). */
export function priceMapFromPublicCatalog(
  publicModels: readonly PublicCatalogModel[],
  entitled: ReadonlySet<string>
): MediaPriceMap {
  const map = new Map<string, MediaPrice>()
  for (const m of publicModels) {
    if (!entitled.has(m.model_id)) continue
    const perImage = m.pricing?.output_cost_per_image
    const perSecond = m.pricing?.output_cost_per_second
    if (perImage != null || perSecond != null) {
      map.set(m.model_id, { perImage: perImage ?? undefined, perSecond: perSecond ?? undefined })
    }
  }
  return map
}
