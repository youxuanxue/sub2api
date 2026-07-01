/**
 * TokenKey-only: helpers for Universal (全能) API keys in the Studio.
 *
 * Per-request routing (video/image/chat) works on universal keys — the gap is
 * Studio discovery: GET /v1/models skips universal resolution (metadata endpoint)
 * and me/pricing-catalog rejects api_key_id when group_id IS NULL.
 */
import type { ApiKey } from '@/types'
import type { MePricingCatalogResponse } from '@/api/me-pricing'

export {
  buildCatalogBillingIndex,
  priceMapFromMeCatalog,
  priceMapFromPublicCatalog,
  type CatalogBillingIndex,
} from '@/utils/studioMediaCatalog.tk'

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
