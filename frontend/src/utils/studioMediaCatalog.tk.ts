/**
 * TokenKey-only: Studio media membership + prices from the public / me pricing
 * catalogs (billing_mode image | video). Presentation metadata stays in
 * studioMediaPresentations.tk.ts — this module must NOT invent model inventories.
 */
import type { MePricingModel } from '@/api/me-pricing'
import type { PublicCatalogModel } from '@/api/pricing'
import type { MediaPrice, MediaPriceMap, StudioModality } from '@/constants/studioMediaPresentations.tk'

export type CatalogBillingIndex = ReadonlyMap<string, StudioModality>

function normalizeBillingMode(raw: string | undefined): StudioModality | undefined {
  if (raw === 'image' || raw === 'video') return raw
  return undefined
}

/** model_id → image | video, derived from GET /api/v1/public/pricing. */
export function buildCatalogBillingIndex(
  publicModels: readonly Pick<PublicCatalogModel, 'model_id' | 'pricing'>[]
): CatalogBillingIndex {
  const map = new Map<string, StudioModality>()
  for (const m of publicModels) {
    const mode = normalizeBillingMode(m.pricing?.billing_mode)
    if (mode) map.set(m.model_id, mode)
  }
  return map
}

export function catalogModalityForModel(
  modelId: string,
  catalogBilling: CatalogBillingIndex
): StudioModality | undefined {
  return catalogBilling.get(modelId)
}

/** Official media prices for entitled models (public catalog ∩ authorized index). */
export function priceMapFromPublicCatalog(
  publicModels: readonly PublicCatalogModel[],
  entitled: ReadonlySet<string>
): MediaPriceMap {
  const map = new Map<string, MediaPrice>()
  for (const m of publicModels) {
    if (!entitled.has(m.model_id)) continue
    const entry = mediaPriceFromCatalogRow(m.pricing?.billing_mode, m.pricing?.output_cost_per_image, m.pricing?.output_cost_per_second, m.vendor)
    if (entry) map.set(m.model_id, entry)
  }
  return map
}

export function priceMapFromMeCatalog(models: readonly MePricingModel[]): MediaPriceMap {
  const map = new Map<string, MediaPrice>()
  for (const m of models) {
    const entry = mediaPriceFromCatalogRow(
      m.billing_mode,
      m.your_price?.per_image,
      m.your_price?.per_second,
      m.vendor
    )
    if (entry) map.set(m.model_id, entry)
  }
  return map
}

function mediaPriceFromCatalogRow(
  billingModeRaw: string | undefined,
  perImage: number | undefined | null,
  perSecond: number | undefined | null,
  vendor: string | undefined
): MediaPrice | undefined {
  const billingMode = normalizeBillingMode(billingModeRaw)
  if (!billingMode) return undefined
  const hasImage = perImage != null && perImage > 0
  const hasVideo = perSecond != null && perSecond > 0
  if ((billingMode === 'image' && !hasImage) || (billingMode === 'video' && !hasVideo)) return undefined
  return {
    perImage: billingMode === 'image' && hasImage ? perImage : undefined,
    perSecond: billingMode === 'video' && hasVideo ? perSecond : undefined,
    billingMode,
    vendor,
  }
}
