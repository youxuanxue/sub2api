/**
 * Client-side filtering for the public pricing catalog (model_id search).
 */

import type { PublicCatalogModel } from '@/api/pricing'

export type PricingCatalogSearchMode = 'fuzzy' | 'exact'

/**
 * Filters catalog rows by model_id.
 * - fuzzy: case-insensitive substring match (partial / "模糊")
 * - exact: case-insensitive full string equality after trim ("精准")
 */
export function filterPricingCatalogByModel(
  models: PublicCatalogModel[],
  rawQuery: string,
  mode: PricingCatalogSearchMode
): PublicCatalogModel[] {
  const q = rawQuery.trim()
  if (!q) {
    return models
  }
  const lower = q.toLowerCase()
  if (mode === 'exact') {
    return models.filter((m) => m.model_id.trim().toLowerCase() === lower)
  }
  return models.filter((m) => m.model_id.toLowerCase().includes(lower))
}
