/**
 * TokenKey-only: helpers for Universal (全能) API keys in Studio / Use Key guide.
 *
 * Per-request routing (video/image/chat) works on universal keys — the gap is
 * discovery UIs: GET /v1/models skips universal resolution (metadata endpoint)
 * and me/pricing-catalog rejects api_key_id when group_id IS NULL.
 */
import type { ApiKey } from '@/types'
import type { MePricingCatalogResponse } from '@/api/me-pricing'
import type { PublicCatalogModel } from '@/api/pricing'

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

export interface UniversalServableModel {
  id: string
  capabilities: string[]
  contextWindow?: number
  maxOutput?: number
}

/** Build a live servable menu for universal keys: entitlement index ∩ public catalog metadata. */
export function servableModelsFromUniversalEntitlement(
  meCatalog: MePricingCatalogResponse | null,
  publicModels: readonly Pick<
    PublicCatalogModel,
    'model_id' | 'capabilities' | 'context_window' | 'max_output_tokens'
  >[],
): UniversalServableModel[] {
  const entitled = entitledModelIds(meCatalog)
  if (entitled.size === 0) return []

  const byId = new Map(publicModels.map((m) => [m.model_id, m]))
  const out: UniversalServableModel[] = []
  for (const id of entitled) {
    const pub = byId.get(id)
    out.push({
      id,
      capabilities: pub?.capabilities ?? [],
      contextWindow: pub?.context_window,
      maxOutput: pub?.max_output_tokens,
    })
  }
  out.sort((a, b) => a.id.localeCompare(b.id))
  return out
}
