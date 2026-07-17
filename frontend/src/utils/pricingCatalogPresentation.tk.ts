export type PricingCatalogModality = 'text' | 'image' | 'video'

export function pricingCatalogModality(billingMode?: string): PricingCatalogModality {
  if (billingMode === 'image') return 'image'
  if (billingMode === 'video') return 'video'
  return 'text'
}

export function formatCatalogPrice(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(6)}`
  if (value < 1) return `$${value.toFixed(4)}`
  return `$${value.toFixed(2)}`
}

export function formatCatalogMediaPrice(value?: number): string {
  if (value == null || value <= 0) return '—'
  return formatCatalogPrice(value)
}
