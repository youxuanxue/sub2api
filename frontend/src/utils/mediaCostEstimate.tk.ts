/**
 * TokenKey-only: client-side media (image/video) cost estimation.
 *
 * Mirrors the backend SETTLEMENT formula so the headline price on the Studio
 * cost panel / Generate button / pricing estimator equals what the user is
 * actually BILLED:
 *   - image: BillingService.CalculateImageCost / getDefaultImagePrice
 *            (backend/internal/service/billing_service.go:891-1005)
 *            unit = base × {1K:1, 2K:1.5, 4K:2}; total = unit × n × rateMultiplier;
 *            no price → $0.134 fallback.
 *   - video: BillingService.CalculateVideoCost (billing_service.go:958-977)
 *            cost = perSecond × seconds × rateMultiplier.
 *   - size tier mirrors ClassifyImageBillingTier / NormalizeImageBillingTierOrDefault
 *     (image_billing_size.go:28-69; empty/unknown → 2K for SETTLEMENT).
 *
 * IMPORTANT — hold ≥ estimate is INTENTIONAL; the pre-flight HOLD is a deliberate
 * UPPER BOUND, not the settlement figure:
 *   - video hold = perSecond × seconds (exact — equals the estimate).
 *   - image hold = the tier-MAX (4K = base×2), and empty size → 4K
 *     (EstimateImageHold / tkEstimateImageHoldAmount) so a request can never
 *     under-reserve. For a 1K/2K request the hold reserves MORE than the per-size
 *     headline. The AFFORDABILITY gate must use estimateImageHoldCost() (below),
 *     NOT the per-size estimate, or the button would enable a request the backend
 *     hold then 403s. Settlement still bills the actual requested size.
 */

export type ImageSizeTier = '1K' | '2K' | '4K'

/** Mirrors getDefaultImagePrice: 2K → ×1.5, 4K → ×2, else ×1. */
export const IMAGE_SIZE_MULTIPLIER: Record<ImageSizeTier, number> = {
  '1K': 1,
  '2K': 1.5,
  '4K': 2,
}

/** $0.134 hard-coded fallback (gemini-3-pro-image-preview) — billing_service.go:993. */
export const IMAGE_FALLBACK_BASE_PRICE = 0.134

/**
 * Client mirror of ClassifyImageBillingTier + NormalizeImageBillingTierOrDefault.
 * Accepts the literal tiers ("1K"/"2K"/"4K", case-insensitive) and WxH pixel
 * strings; empty/"auto"/unknown normalize to "2K" (the backend default — the
 * reason the UI never offers a deceptive "auto = cheapest").
 */
export function classifyImageBillingTier(size: string | undefined | null): ImageSizeTier {
  const trimmed = (size || '').trim().toLowerCase()
  switch (trimmed) {
    case '':
    case 'auto':
      return '2K'
    case '1k':
      return '1K'
    case '2k':
      return '2K'
    case '4k':
      return '4K'
    case '2048x2048':
    case '2048x1152':
      return '2K'
    case '3840x2160':
    case '2160x3840':
      return '4K'
  }
  const parts = trimmed.split('x')
  if (parts.length === 2) {
    const w = Number.parseInt(parts[0].trim(), 10)
    const h = Number.parseInt(parts[1].trim(), 10)
    if (Number.isFinite(w) && Number.isFinite(h) && w > 0 && h > 0) {
      const maxEdge = Math.max(w, h)
      if (maxEdge <= 1024) return '1K'
      if (maxEdge <= 2048) return '2K'
      return '4K'
    }
  }
  return '2K'
}

function clampRate(rateMultiplier: number): number {
  return rateMultiplier < 0 ? 0 : rateMultiplier
}

export interface ImageCostInput {
  /** output_cost_per_image at the 1K base tier (USD). */
  baseImagePrice: number
  /** "1K" | "2K" | "4K", or a raw size string (classified via classifyImageBillingTier). */
  size: ImageSizeTier | string
  /** requested image count (n); ≤0 treated as 1. */
  n: number
  /** effective group × override rate; defaults to 1 (list price). */
  rateMultiplier?: number
}

/** estimateImageCost mirrors CalculateImageCost (USD). */
export function estimateImageCost(input: ImageCostInput): number {
  const count = input.n > 0 ? input.n : 1
  const base = input.baseImagePrice > 0 ? input.baseImagePrice : IMAGE_FALLBACK_BASE_PRICE
  const tier = (['1K', '2K', '4K'] as const).includes(input.size as ImageSizeTier)
    ? (input.size as ImageSizeTier)
    : classifyImageBillingTier(input.size)
  const unit = base * IMAGE_SIZE_MULTIPLIER[tier]
  return unit * count * clampRate(input.rateMultiplier ?? 1)
}

/**
 * Upper bound matching the backend pre-flight HOLD (EstimateImageHold /
 * tkEstimateImageHoldAmount): always the dearest tier (4K = base×2), regardless
 * of the requested size. The hold RESERVES this amount; settlement bills the real
 * size tier (estimateImageCost). Gate affordability on THIS — never on the
 * per-size estimate — so the UI cannot enable a request the hold will 403.
 */
export function estimateImageHoldCost(input: Omit<ImageCostInput, 'size'>): number {
  const count = input.n > 0 ? input.n : 1
  const base = input.baseImagePrice > 0 ? input.baseImagePrice : IMAGE_FALLBACK_BASE_PRICE
  return base * IMAGE_SIZE_MULTIPLIER['4K'] * count * clampRate(input.rateMultiplier ?? 1)
}

export interface VideoCostInput {
  /** output_cost_per_second (USD). */
  perSecond: number
  /** requested duration in seconds; ≤0 treated as 1. */
  seconds: number
  /** effective group × override rate; defaults to 1 (list price). */
  rateMultiplier?: number
}

/** estimateVideoCost mirrors CalculateVideoCost (USD). */
export function estimateVideoCost(input: VideoCostInput): number {
  const s = input.seconds > 0 ? input.seconds : 1
  const perSecond = input.perSecond > 0 ? input.perSecond : 0
  return perSecond * s * clampRate(input.rateMultiplier ?? 1)
}

/**
 * Format a USD amount for display. Sub-dollar amounts keep up to 4 decimals
 * with trailing zeros trimmed (min 2) so sub-cent media prices like $0.0299
 * stay precise while $0.06 / $3.20 stay clean; ≥$1 uses 2 decimals. Always
 * prefixed "$"; 0 renders "$0".
 */
export function formatUsd(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  if (Math.abs(value) < 1) {
    let s = value.toFixed(4).replace(/0+$/, '')
    if (/\.\d$/.test(s)) s += '0' // ensure at least 2 decimals (e.g. "0.5" → "0.50")
    if (!s.includes('.')) s += '.00'
    return `$${s}`
  }
  return `$${value.toFixed(2)}`
}
