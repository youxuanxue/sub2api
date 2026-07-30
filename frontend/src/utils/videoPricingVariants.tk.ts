/**
 * TokenKey: single source of truth for rendering **video price tiers** in catalog
 * surfaces (model marketplace cards, /models?view=pricing table, and any future
 * consumer that must not drift).
 *
 * When `video_price_tiers` is present, headline shows the min–max USD/s range and
 * each billable bracket gets its own labelled line — same idea as token tiered
 * pricing in pricingVariants.tk.ts.
 */

import type { PublicPricingVideoTier } from '@/api/pricing'
import { formatCatalogMediaPrice, formatCatalogPrice } from '@/utils/pricingCatalogPresentation.tk'

export type VideoPricingVariantTranslate = (key: string, params?: Record<string, unknown>) => string

export interface VideoPricingVariantLine {
  label: string
  perSecond: number
}

export interface VideoPricingVariantView {
  kind: 'flat' | 'tiered'
  /** USD/s range for tiered models; null when flat or unpriced. */
  minPerSecond: number | null
  maxPerSecond: number | null
  /** Empty for flat-priced video models. */
  lines: VideoPricingVariantLine[]
}

export interface VideoPricingVariantInput {
  output_cost_per_second?: number
  video_price_tiers?: PublicPricingVideoTier[]
}

function collectBillableRates(tiers: readonly PublicPricingVideoTier[]): number[] {
  const rates: number[] = []
  for (const tier of tiers) {
    if (tier.per_second > 0) rates.push(tier.per_second)
    if (tier.per_second_silent != null && tier.per_second_silent > 0) {
      rates.push(tier.per_second_silent)
    }
    if (
      tier.input_image_surcharge_per_second != null &&
      tier.input_image_surcharge_per_second > 0 &&
      tier.per_second > 0
    ) {
      rates.push(tier.per_second + tier.input_image_surcharge_per_second)
    }
  }
  return rates
}

function formatResolutionLabel(resolution: string): string {
  const r = resolution.trim()
  if (!r) return r
  if (/^\d+k$/i.test(r)) return r.toUpperCase()
  return r
}

function buildTierLines(
  tiers: readonly PublicPricingVideoTier[],
  t: VideoPricingVariantTranslate,
): VideoPricingVariantLine[] {
  const lines: VideoPricingVariantLine[] = []
  for (const tier of tiers) {
    const res = formatResolutionLabel(tier.resolution)
    const hasSilent = tier.per_second_silent != null && tier.per_second_silent > 0
    const hasSurcharge =
      tier.input_image_surcharge_per_second != null && tier.input_image_surcharge_per_second > 0

    if (tier.per_second > 0) {
      const suffix =
        hasSilent && !hasSurcharge
          ? ` · ${t('pricing.video.withAudio')}`
          : hasSurcharge
            ? ` · ${t('pricing.video.textToVideo')}`
            : ''
      lines.push({ label: `${res}${suffix}`, perSecond: tier.per_second })
    }

    if (hasSilent) {
      lines.push({
        label: `${res} · ${t('pricing.video.withoutAudio')}`,
        perSecond: tier.per_second_silent!,
      })
    }

    if (hasSurcharge && tier.per_second > 0) {
      lines.push({
        label: `${res} · ${t('pricing.video.withInputImage')}`,
        perSecond: tier.per_second + tier.input_image_surcharge_per_second!,
      })
    }
  }
  return lines
}

/** Resolve headline + tier lines for a catalog video model. Pure + translator-injected. */
export function resolveVideoPricingVariant(
  pricing: VideoPricingVariantInput | undefined,
  t: VideoPricingVariantTranslate,
): VideoPricingVariantView {
  const flat = pricing?.output_cost_per_second
  const tiers = pricing?.video_price_tiers
  if (!tiers?.length) {
    return {
      kind: 'flat',
      minPerSecond: flat != null && flat > 0 ? flat : null,
      maxPerSecond: flat != null && flat > 0 ? flat : null,
      lines: [],
    }
  }

  const rates = collectBillableRates(tiers)
  const min = rates.length ? Math.min(...rates) : null
  const max = rates.length ? Math.max(...rates) : null
  return {
    kind: 'tiered',
    minPerSecond: min,
    maxPerSecond: max,
    lines: buildTierLines(tiers, t),
  }
}

/** Headline USD/s for marketplace cards: range when tiered, single price when flat. */
export function formatCatalogVideoHeadline(
  view: VideoPricingVariantView,
  t: VideoPricingVariantTranslate,
): string {
  const { minPerSecond, maxPerSecond } = view
  if (minPerSecond == null || minPerSecond <= 0) return '—'
  if (view.kind === 'tiered' && maxPerSecond != null && maxPerSecond > minPerSecond) {
    return t('pricing.variant.tierRange', {
      lo: formatCatalogPrice(minPerSecond),
      hi: formatCatalogPrice(maxPerSecond),
    })
  }
  return formatCatalogMediaPrice(minPerSecond)
}
