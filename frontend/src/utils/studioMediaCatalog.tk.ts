/**
 * TokenKey-only: Studio media **price enrichment** from public / me pricing
 * catalogs (`billing_mode` image | video). Membership (which models a key may
 * list) is owned by the entitlement pool + `modalityForModel` in
 * studioMediaPresentations.tk.ts — this module must NOT invent inventories.
 */
import type { PublicCatalogModel, PublicPricingVideoTier } from '@/api/pricing'
import type { MePricingModel } from '@/api/me-pricing'
import type { MediaPrice, MediaPriceMap, StudioModality } from '@/constants/studioMediaPresentations.tk'
import type { VideoPriceTier } from '@/utils/mediaCostEstimate.tk'
import { pricingCatalogModality } from '@/utils/pricingCatalogPresentation.tk'

export type CatalogBillingIndex = ReadonlyMap<string, StudioModality>

/** model_id → image | video, derived from GET /api/v1/public/pricing. */
export function buildCatalogBillingIndex(
  publicModels: readonly Pick<PublicCatalogModel, 'model_id' | 'pricing'>[]
): CatalogBillingIndex {
  const map = new Map<string, StudioModality>()
  for (const m of publicModels) {
    const mode = pricingCatalogModality(m.pricing?.billing_mode)
    if (mode === 'image' || mode === 'video') map.set(m.model_id, mode)
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
    const entry = mediaPriceFromCatalogRow(
      m.pricing?.billing_mode,
      m.pricing?.output_cost_per_image,
      m.pricing?.output_cost_per_second,
      m.vendor,
      m.pricing?.video_price_tiers,
      m.pricing
    )
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
      m.vendor,
      m.your_price?.video_price_tiers
    )
    if (entry) map.set(m.model_id, entry)
  }
  return map
}

function mediaPriceFromCatalogRow(
  billingModeRaw: string | undefined,
  perImage: number | undefined | null,
  perSecond: number | undefined | null,
  vendor: string | undefined,
  videoTiersRaw?: readonly PublicPricingVideoTier[],
  imageTiers?: { image_price_1k?: number; image_price_2k?: number; image_price_4k?: number }
): MediaPrice | undefined {
  const billingMode = pricingCatalogModality(billingModeRaw)
  if (billingMode !== 'image' && billingMode !== 'video') return undefined
  const hasImage = perImage != null && perImage > 0
  const hasVideo = perSecond != null && perSecond > 0
  if ((billingMode === 'image' && !hasImage) || (billingMode === 'video' && !hasVideo && !videoTiersRaw?.length)) return undefined
  const videoTiers = mapVideoPriceTiers(videoTiersRaw)
  const entry: MediaPrice = {
    perImage: billingMode === 'image' && hasImage ? perImage : undefined,
    perSecond: billingMode === 'video' && hasVideo ? perSecond : undefined,
    videoTiers: billingMode === 'video' ? videoTiers : undefined,
    billingMode,
    vendor,
  }
  const imagePrice1K = positiveTier(imageTiers?.image_price_1k)
  const imagePrice2K = positiveTier(imageTiers?.image_price_2k)
  const imagePrice4K = positiveTier(imageTiers?.image_price_4k)
  if (imagePrice1K != null) entry.imagePrice1K = imagePrice1K
  if (imagePrice2K != null) entry.imagePrice2K = imagePrice2K
  if (imagePrice4K != null) entry.imagePrice4K = imagePrice4K
  return entry
}

function positiveTier(price: number | undefined): number | undefined {
  return price != null && price > 0 ? price : undefined
}

/** Copy explicit image tiers from the public catalog onto a price map that only has per-image. */
export function attachPublicImageTiers(
  map: MediaPriceMap,
  publicModels: readonly PublicCatalogModel[]
): MediaPriceMap {
  const tiers = new Map<string, Pick<MediaPrice, 'imagePrice1K' | 'imagePrice2K' | 'imagePrice4K'>>()
  for (const m of publicModels) {
    const imagePrice1K = positiveTier(m.pricing?.image_price_1k)
    const imagePrice2K = positiveTier(m.pricing?.image_price_2k)
    const imagePrice4K = positiveTier(m.pricing?.image_price_4k)
    if (imagePrice1K == null && imagePrice2K == null && imagePrice4K == null) continue
    tiers.set(m.model_id, { imagePrice1K, imagePrice2K, imagePrice4K })
  }
  if (tiers.size === 0) return map
  const out = new Map(map)
  for (const [id, row] of out) {
    const tier = tiers.get(id)
    if (tier) out.set(id, { ...row, ...tier })
  }
  return out
}

function mapVideoPriceTiers(raw?: readonly PublicPricingVideoTier[]): VideoPriceTier[] | undefined {
  if (!raw?.length) return undefined
  return raw.map((t) => ({
    resolution: t.resolution,
    perSecond: t.per_second,
    perSecondSilent: t.per_second_silent,
    inputImageSurchargePerSecond: t.input_image_surcharge_per_second,
    defaultForModel: t.default_for_model,
  }))
}
