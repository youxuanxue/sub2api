/**
 * TokenKey: single source of truth for rendering **price variants** — models
 * whose unit price is not one flat number.
 *
 * Two variants exist today, and they are the same idea (price varies by a
 * condition), so they get one shape instead of two bespoke widgets:
 *
 * - **tiered (阶梯)** — unit price depends on request context length. Backed by
 *   overlay `intervals` (VolcEngine doubao-seed-*, Qwen-plus/coder, GLM-4.x/5.x),
 *   surfaced as `pricing.tiers` by `attachCatalogOverlayTiers`.
 * - **peak/valley (峰谷)** — unit price depends on time of day. Backed by
 *   `_config.deepseek_peak_valley`, surfaced as `pricing.peak_valley` by
 *   `attachCatalogDeepSeekPeakValley`. The catalog's flat fields are the
 *   OFF-PEAK (谷时) price; the peak price is flat × multiplier.
 *
 * Both resolve to an ordered list of `PricingVariantLine`s — a labelled price
 * row. Every consumer renders the same lines in the same order, so `/pricing`
 * and the `/models` cards cannot drift apart. Callers must render one visual
 * line per entry across all price columns; alignment between the input, output
 * and cache columns is guaranteed by construction (same list, same order).
 *
 * Why this exists: before it, both surfaces showed only the FIRST tier / the
 * off-peak price as if it were the price. doubao-seed-2-0-pro's top tier is 3×
 * the first, qwen-plus's top output tier is 24× — the displayed number was off
 * by an order of magnitude for long-context requests.
 *
 * Pure + translator-injected so it unit-tests without a Vue app.
 */

/** vue-i18n's `t`, narrowed to what this module needs. */
export type PricingVariantTranslate = (key: string, params?: Record<string, unknown>) => string

/**
 * One context-length bracket. Matching is **left-open, right-closed** `(min, max]`
 * — see `FindMatchingInterval` in backend/internal/service/channel.go, which is
 * the billing behaviour these labels must describe. `maxTokens: null` is the
 * open-ended top bracket.
 */
export interface PricingVariantTier {
  minTokens: number
  maxTokens: number | null
  inputPer1K: number | null
  outputPer1K: number | null
  cacheReadPer1K: number | null
}

/** Peak-window pricing. Prices here are the PEAK side; off-peak is the flat row. */
export interface PricingVariantPeakValley {
  /** IANA zone the windows are evaluated in, e.g. "Asia/Shanghai". */
  timezone: string
  /** Windows as `"HH:MM-HH:MM"`, verbatim from the backend. */
  windows: string[]
  peakMultiplier: number
  inputPer1K: number
  outputPer1K: number
  cacheReadPer1K: number | null
}

/** Flat (single-price) inputs, used as the off-peak row of a peak/valley model. */
export interface PricingVariantFlat {
  inputPer1K: number | null
  outputPer1K: number | null
  cacheReadPer1K: number | null
}

export interface PricingVariantLine {
  /** Short label for this line, e.g. "≤32k" / ">128k" / "谷时" / "高峰". */
  label: string
  inputPer1K: number | null
  outputPer1K: number | null
  cacheReadPer1K: number | null
}

export type PricingVariantKind = 'flat' | 'tiered' | 'peak_valley'

export interface PricingVariantView {
  kind: PricingVariantKind
  /** Empty for `flat` — callers keep their existing single-price markup. */
  lines: PricingVariantLine[]
  /**
   * One-line explanation of WHY the price varies, e.g. the peak windows and
   * their timezone. Empty for `flat`. Belongs next to the model name, not in a
   * price column: it is prose, and must not participate in row alignment.
   */
  caption: string
}

/**
 * The "no variant" result: a single flat price. Frozen and shared — callers use
 * it as the fallback for a row that has no resolved variant, and must not mutate it.
 */
export const FLAT_PRICING_VARIANT: PricingVariantView = Object.freeze({
  kind: 'flat',
  lines: [] as PricingVariantLine[],
  caption: ''
})

const FLAT_VIEW = FLAT_PRICING_VARIANT

/**
 * Compact token bound: 32000 → "32k", 1000000 → "1M", 1500 → "1500".
 * Exported for the CSV export, which needs the same bounds as the UI.
 */
export function formatTokenBound(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return String(n)
  if (n % 1_000_000 === 0) return `${n / 1_000_000}M`
  if (n % 1000 === 0) return `${n / 1000}k`
  return String(n)
}

/**
 * Label for one bracket, phrased to match `(min, max]` billing:
 * `(0, 32k]` → "≤32k", `(32k, 128k]` → "32k–128k", `(128k, ∞)` → ">128k".
 *
 * Deliberately NOT the old "0–32k" form: that reads as left-closed/right-open
 * and told users a 32 000-token request lands in the second bracket when
 * billing puts it in the first.
 */
export function formatTierLabel(
  tier: Pick<PricingVariantTier, 'minTokens' | 'maxTokens'>,
  t: PricingVariantTranslate
): string {
  const { minTokens, maxTokens } = tier
  const hasFloor = Number.isFinite(minTokens) && minTokens > 0
  if (!hasFloor && maxTokens != null) {
    return t('pricing.variant.tierUpTo', { bound: formatTokenBound(maxTokens) })
  }
  if (hasFloor && maxTokens == null) {
    return t('pricing.variant.tierAbove', { bound: formatTokenBound(minTokens) })
  }
  if (hasFloor && maxTokens != null) {
    return t('pricing.variant.tierRange', {
      lo: formatTokenBound(minTokens),
      hi: formatTokenBound(maxTokens)
    })
  }
  // Unbounded on both ends — not a real bracket; caller filters these out.
  return ''
}

/** "09:00-12:00" → "09:00–12:00" (en dash reads as a range, not a minus). */
function formatWindow(window: string): string {
  return window.replace('-', '–')
}

/**
 * Resolve a model's price variant into display lines.
 *
 * `tiers` wins when both are present. That combination does not exist in the
 * overlay today (no model is both tiered and peak-priced) and rendering the
 * 3×2 cross-product would be unreadable; a mechanical test asserts the overlay
 * never introduces one, so this branch is a safety net rather than a policy.
 */
export function resolvePricingVariant(
  input: {
    flat: PricingVariantFlat
    tiers?: PricingVariantTier[] | null
    peakValley?: PricingVariantPeakValley | null
  },
  t: PricingVariantTranslate
): PricingVariantView {
  const { flat, tiers, peakValley } = input

  if (tiers && tiers.length > 1) {
    const lines: PricingVariantLine[] = []
    for (const tier of tiers) {
      const label = formatTierLabel(tier, t)
      if (!label) continue
      lines.push({
        label,
        inputPer1K: tier.inputPer1K,
        outputPer1K: tier.outputPer1K,
        cacheReadPer1K: tier.cacheReadPer1K
      })
    }
    // A single usable bracket is just a flat price wearing a ladder's clothes.
    if (lines.length < 2) return FLAT_VIEW
    return { kind: 'tiered', lines, caption: t('pricing.variant.tieredCaption') }
  }

  if (peakValley && peakValley.peakMultiplier > 1) {
    const windows = peakValley.windows.filter(Boolean).map(formatWindow)
    if (windows.length === 0) return FLAT_VIEW
    return {
      kind: 'peak_valley',
      lines: [
        {
          label: t('pricing.variant.offPeak'),
          inputPer1K: flat.inputPer1K,
          outputPer1K: flat.outputPer1K,
          cacheReadPer1K: flat.cacheReadPer1K
        },
        {
          label: t('pricing.variant.peak'),
          inputPer1K: peakValley.inputPer1K,
          outputPer1K: peakValley.outputPer1K,
          cacheReadPer1K: peakValley.cacheReadPer1K
        }
      ],
      caption: t('pricing.variant.peakCaption', {
        windows: windows.join(', '),
        tz: peakValley.timezone,
        mult: peakValley.peakMultiplier
      })
    }
  }

  return FLAT_VIEW
}
