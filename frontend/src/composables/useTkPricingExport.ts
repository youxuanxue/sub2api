/**
 * TokenKey: admin-only "export platform pricing" for the public /pricing page.
 *
 * Builds a sales-friendly CSV from the public catalog (在售目录 / 对外价) the page
 * already fetched — no extra backend call. Prices are emitted per 1,000,000 tokens
 * (the sales口径).
 *
 * A model whose price is not one flat number stays ONE row: the context-length
 * (阶梯) ladder collapses into the readable `tiers` column, and time-of-day (峰谷)
 * pricing into the `peak_*` columns. The flat `input_per_1M` / `output_per_1M`
 * cells are the first tier / the off-peak price respectively — same convention as
 * the catalog API, so the sheet cannot disagree with the /pricing table. Bracket
 * bounds come from the shared owner (pricingVariants.tk.ts) so a label change
 * lands in the UI and the sheet together.
 *
 * This composable owns the CSV + download logic; PricingView.vue stays template +
 * a single admin-gated button (TokenKey upstream-isolation pattern, CLAUDE.md §5).
 */

import type {
  PublicCatalogModel,
  PublicCatalogResponse,
  PublicPricingPeakValley,
  PublicPricingTier
} from '@/api/pricing'
import {
  catalogTokenPricePer1M,
  formatCatalogUsdNumeric
} from '@/utils/pricingCatalogPresentation.tk'
import { formatTokenBound } from '@/utils/pricingVariants.tk'

const CSV_COLUMNS = [
  'model_id',
  'vendor',
  'billing_mode',
  'currency',
  'input_per_1M',
  'output_per_1M',
  'thinking_output_per_1M',
  'cache_read_per_1M',
  'cache_write_per_1M',
  'price_per_image_USD',
  'price_per_second_USD',
  'tiers',
  'peak_windows',
  'peak_timezone',
  'peak_multiplier',
  'peak_input_per_1M',
  'peak_output_per_1M',
  'peak_cache_read_per_1M',
  'context_window',
  'max_output_tokens',
  'capabilities'
] as const

/** per-1k → per-1M; same precision as catalog UI (formatCatalogUsd). */
function per1M(per1k: number | undefined | null): string {
  if (per1k === undefined || per1k === null || per1k === 0) return ''
  return formatCatalogUsdNumeric(catalogTokenPricePer1M(per1k))
}

/** USD per unit (image/second); same precision as catalog UI. */
function unitUsd(v: number | undefined | null): string {
  if (v === undefined || v === null || v === 0) return ''
  return formatCatalogUsdNumeric(v)
}

/**
 * "32000" → "32k", 0 → "0", non-round → raw. undefined = "∞" (open-ended top
 * bracket). Bounds come from the shared variant module so a bracket reads the
 * same in the sheet as on the page.
 */
function tokenBound(n: number | undefined): string {
  if (n === undefined) return '∞'
  if (n === 0) return '0'
  return formatTokenBound(n)
}

/**
 * Render the阶梯 ladder into one readable cell (prices per 1M USD).
 *
 * Brackets are left-open, right-closed — `(lo, hi]` — matching billing
 * (FindMatchingInterval, backend/internal/service/channel.go). Includes each
 * bracket's cache-read price: it varies per bracket too, and omitting it left the
 * sheet implying the flat first-tier cache price applied at every length.
 */
export function formatTiers(tiers: PublicPricingTier[] | undefined): string {
  if (!tiers || tiers.length === 0) return ''
  return tiers
    .map((t) => {
      const lo = tokenBound(t.min_tokens)
      const hi = tokenBound(t.max_tokens)
      const inp = per1M(t.input_per_1k_tokens)
      const out = per1M(t.output_per_1k_tokens)
      const cache = per1M(t.cache_read_per_1k)
      // Right-closed on a finite bound, open at ∞ — "(128k, ∞]" would claim an
      // inclusive infinity.
      const bracket = t.max_tokens === undefined ? `(${lo}, ${hi})` : `(${lo}, ${hi}]`
      const base = `${bracket}: in ${inp} / out ${out}`
      return cache ? `${base} / cache ${cache}` : base
    })
    .join(' | ')
}

/** Peak windows as one cell, e.g. "09:00-12:00; 14:00-18:00". */
function formatPeakWindows(pv: PublicPricingPeakValley | undefined): string {
  if (!pv || !pv.windows || pv.windows.length === 0) return ''
  return pv.windows.filter(Boolean).join('; ')
}

/** RFC-4180 escaping: quote when the cell holds a comma, quote, or newline. */
function csvEscape(value: string): string {
  if (/[",\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`
  }
  return value
}

function rowFor(m: PublicCatalogModel): string[] {
  const p = m.pricing
  return [
    m.model_id ?? '',
    m.vendor ?? '',
    p.billing_mode || 'token',
    p.currency ?? 'USD',
    per1M(p.input_per_1k_tokens),
    per1M(p.output_per_1k_tokens),
    per1M(p.thinking_output_per_1k_tokens),
    per1M(p.cache_read_per_1k),
    per1M(p.cache_write_per_1k),
    unitUsd(p.output_cost_per_image),
    unitUsd(p.output_cost_per_second),
    formatTiers(p.tiers),
    formatPeakWindows(p.peak_valley),
    p.peak_valley?.timezone ?? '',
    p.peak_valley?.peak_multiplier ? String(p.peak_valley.peak_multiplier) : '',
    per1M(p.peak_valley?.input_per_1k_tokens),
    per1M(p.peak_valley?.output_per_1k_tokens),
    per1M(p.peak_valley?.cache_read_per_1k),
    m.context_window ? String(m.context_window) : '',
    m.max_output_tokens ? String(m.max_output_tokens) : '',
    (m.capabilities ?? []).join(';')
  ]
}

/**
 * Build the full CSV text (incl. header). Exported for unit testing. Models are
 * sorted by (vendor, model_id) for a stable, scannable sheet.
 */
export function buildPricingCsv(catalog: PublicCatalogResponse | null): string {
  const models = [...(catalog?.data ?? [])].sort((a, b) => {
    const va = a.vendor ?? ''
    const vb = b.vendor ?? ''
    return va === vb ? a.model_id.localeCompare(b.model_id) : va.localeCompare(vb)
  })
  const lines = [CSV_COLUMNS.join(',')]
  for (const m of models) {
    lines.push(rowFor(m).map(csvEscape).join(','))
  }
  return lines.join('\r\n')
}

/** Build the dated download filename, e.g. tokenkey-pricing-2026-06-26.csv. */
export function pricingCsvFilename(now: Date = new Date()): string {
  return `tokenkey-pricing-${now.toISOString().split('T')[0]}.csv`
}

/**
 * Trigger a browser download of the public pricing catalog as CSV. A leading
 * UTF-8 BOM keeps Excel from garbling the中文 capability/vendor cells.
 */
export function exportPricingCsv(catalog: PublicCatalogResponse | null): void {
  const csv = '﻿' + buildPricingCsv(catalog)
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' })
  const url = window.URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = pricingCsvFilename()
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  window.URL.revokeObjectURL(url)
}
