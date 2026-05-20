/**
 * Client-side filtering for any model-keyed row by model_id.
 *
 * Generic over `T extends { model_id: string }` so the same matcher serves
 * both the public LiteLLM catalog (PublicCatalogModel) and the per-user
 * "your menu" rows. Keeping the constraint to the single field actually
 * used by the matcher avoids the previous PublicCatalogModel coupling
 * that forced callers to shape-shift their rows just to reuse this util.
 */

export type PricingCatalogSearchMode = 'fuzzy' | 'exact'

/**
 * Filters rows by model_id.
 * - fuzzy: case-insensitive substring match (partial / "模糊")
 * - exact: case-insensitive full string equality after trim ("精准")
 */
export function filterPricingCatalogByModel<T extends { model_id: string }>(
  models: T[],
  rawQuery: string,
  mode: PricingCatalogSearchMode
): T[] {
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
