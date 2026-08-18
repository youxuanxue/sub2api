/** TokenKey-only helpers for automatic-routing API keys in Studio. */
import type { ApiKey } from '@/types'

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
