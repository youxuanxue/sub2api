export type PricingCatalogModality = 'text' | 'image' | 'video'

/** Catalog API stores token prices per 1K; UI displays per 1M for readability. */
export const CATALOG_TOKEN_DISPLAY_UNIT = 1_000_000 as const
export const CATALOG_TOKEN_STORAGE_UNIT = 1_000 as const

export function pricingCatalogModality(billingMode?: string): PricingCatalogModality {
  if (billingMode === 'image') return 'image'
  if (billingMode === 'video') return 'video'
  return 'text'
}

/** Convert stored per-1K token price to per-1M display amount. */
export function catalogTokenPricePer1M(per1k: number): number {
  return per1k * (CATALOG_TOKEN_DISPLAY_UNIT / CATALOG_TOKEN_STORAGE_UNIT)
}

/**
 * Single catalog USD display rule (marketplace + pricing table + export labels).
 * At most three fractional digits; trailing zeros trimmed. Billing/Studio keeps
 * its own precision — catalog is for comparison, not invoicing.
 */
export function formatCatalogUsd(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  const s = value.toFixed(3).replace(/\.?0+$/, '')
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
