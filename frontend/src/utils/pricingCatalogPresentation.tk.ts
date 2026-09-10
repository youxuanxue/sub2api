export type PricingCatalogModality = 'text' | 'image' | 'video' | 'embedding' | 'audio'

/** Catalog API stores token prices per 1K; UI displays per 1M for readability. */
export const CATALOG_TOKEN_DISPLAY_UNIT = 1_000_000 as const
export const CATALOG_TOKEN_STORAGE_UNIT = 1_000 as const

export function pricingCatalogModality(billingMode?: string): PricingCatalogModality {
  if (billingMode === 'image') return 'image'
  if (billingMode === 'video') return 'video'
  if (billingMode === 'embedding') return 'embedding'
  if (billingMode === 'tts' || billingMode === 'stt') return 'audio'
  return 'text'
}

export function catalogAudioPrice(mode?: string, perCharacter?: number | null, perInputSecond?: number | null) {
  if (mode === 'tts') return { value: perCharacter == null ? undefined : perCharacter * 10_000, unit: 'pricing.perTenThousandCharacters' }
  if (mode === 'stt') return { value: perInputSecond == null ? undefined : perInputSecond * 3600, unit: 'pricing.perHour' }
  return undefined
}

/** Convert stored per-1K token price to per-1M display amount. */
export function catalogTokenPricePer1M(per1k: number): number {
  return per1k * (CATALOG_TOKEN_DISPLAY_UNIT / CATALOG_TOKEN_STORAGE_UNIT)
}

/**
 * Single catalog USD display rule (marketplace + pricing table + export labels).
 * At most three fractional digits for ordinary amounts; sub-cent prices keep six
 * so catalog and CSV do not materially understate low media tiers.
 */
export function formatCatalogUsdNumeric(value: number): string {
  if (!Number.isFinite(value) || value === 0) return ''
  const digits = Math.abs(value) < 0.01 ? 6 : 3
  return value.toFixed(digits).replace(/\.?0+$/, '')
}

export function formatCatalogUsd(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  const s = formatCatalogUsdNumeric(value)
  return `$${s}`
}

/**
 * Format a catalog token price (stored per 1K) for display as USD / 1M tokens.
 */
export function formatCatalogTokenPrice(per1k: number): string {
  if (!Number.isFinite(per1k)) return '—'
  if (per1k === 0) return '$0'
  return formatCatalogUsd(catalogTokenPricePer1M(per1k))
}

/** USD formatter for image/video/per-request catalog units. */
export function formatCatalogPrice(value: number): string {
  return formatCatalogUsd(value)
}

export function formatCatalogMediaPrice(value?: number): string {
  if (value == null || value <= 0) return '—'
  return formatCatalogUsd(value)
}
