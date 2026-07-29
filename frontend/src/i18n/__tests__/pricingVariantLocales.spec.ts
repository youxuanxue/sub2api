import { describe, expect, it } from 'vitest'
import { baseCompile } from '@intlify/message-compiler'

import overlay from '../tk/legacyMissing.tk'
import {
  formatTierLabel,
  resolvePricingVariant,
  type PricingVariantTranslate
} from '@/utils/pricingVariants.tk'

/**
 * Wiring guard for the price-variant (阶梯 / 峰谷) labels on /pricing and the
 * /models cards.
 *
 * `pricingVariants.tk.spec.ts` proves the resolver picks the right lines, but it
 * injects a fake `t` that echoes whatever key it is handed — so it stays green
 * even when the key exists in no locale at all. That is exactly how this broke:
 * the resolver asks for `pricing.variant.tierUpTo` / `tierRange` / `tieredCaption`
 * / `offPeak` / `peakCaption`, while the overlay carried an earlier badge-era
 * naming (`upTo`, `between`, `tieredNote`, `offPeakLabel`, …). vue-i18n falls
 * back to echoing the key, so every tiered and peak-priced row rendered literal
 * "pricing.variant.tierUpTo" text where a price label belonged.
 *
 * The `t` below is the real overlay behind a lookup that MISSES LOUDLY instead of
 * echoing, so a rename on either side fails here. It is not vue-i18n's `t`: this
 * app pins the runtime-only vue-i18n build (CSP: no eval — see the alias in
 * vitest.config.ts / vite.config.ts), which cannot compile messages in-process,
 * so `t()` under vitest returns the key for *every* message and would make the
 * "not a raw key" assertion vacuous. Interpolation is applied here the same way
 * (named `{param}` substitution), and every message is additionally run through
 * the real `baseCompile` so a malformed placeholder still fails.
 */

type Dict = Record<string, unknown>

/** Resolve a dotted key against the overlay, or undefined when absent. */
function lookup(messages: Dict, key: string): string | undefined {
  let node: unknown = messages
  for (const part of key.split('.')) {
    if (typeof node !== 'object' || node === null) return undefined
    node = (node as Dict)[part]
  }
  return typeof node === 'string' ? node : undefined
}

/**
 * `t` over the real overlay. A missing key throws rather than echoing — the whole
 * point of this suite. Falls back to `en` like the app's `fallbackLocale`.
 */
function makeT(locale: 'en' | 'zh'): PricingVariantTranslate {
  return (key, params) => {
    const raw = lookup(overlay[locale] as Dict, key) ?? lookup(overlay.en as Dict, key)
    if (raw === undefined) {
      throw new Error(
        `locale "${locale}" has no message for "${key}" — pricing.variant keys in ` +
          'legacyMissing.tk.ts drifted from the keys pricingVariants.tk.ts asks for'
      )
    }
    // Fail on a message vue-i18n itself could not compile at render time.
    const compileErrors: string[] = []
    baseCompile(raw, { onError: (err) => compileErrors.push(err.message) })
    expect(compileErrors, `${locale}.${key} does not compile`).toEqual([])

    if (!params) return raw
    return raw.replace(/\{(\w+)\}/g, (whole, name: string) =>
      name in params ? String(params[name]) : whole
    )
  }
}

/** Every user-visible string a variant view puts on screen. */
function renderedStrings(view: ReturnType<typeof resolvePricingVariant>): string[] {
  return [view.caption, ...view.lines.map((l) => l.label)].filter((s) => s.length > 0)
}

const flat = { inputPer1K: 0.0002, outputPer1K: 0.0008, cacheReadPer1K: 0.00002 }

// Shapes mirroring the two real variants in the catalog today: a doubao-style
// three-bracket ladder, and DeepSeek's off-peak/peak windows.
const TIERS = [
  { minTokens: 0, maxTokens: 32000, inputPer1K: 0.0002, outputPer1K: 0.0008, cacheReadPer1K: null },
  {
    minTokens: 32000,
    maxTokens: 128000,
    inputPer1K: 0.0004,
    outputPer1K: 0.0016,
    cacheReadPer1K: null
  },
  {
    minTokens: 128000,
    maxTokens: null,
    inputPer1K: 0.0008,
    outputPer1K: 0.0024,
    cacheReadPer1K: null
  }
]

const PEAK_VALLEY = {
  timezone: 'Asia/Shanghai',
  windows: ['09:00-12:00', '14:00-18:00'],
  peakMultiplier: 2,
  inputPer1K: 0.0004,
  outputPer1K: 0.0016,
  cacheReadPer1K: 0.00004
}

/** The exact key set `pricingVariants.tk.ts` asks for. */
const CALLED_KEYS = [
  'offPeak',
  'peak',
  'peakCaption',
  'tierAbove',
  'tierRange',
  'tierUpTo',
  'tieredCaption'
]

describe('pricing.variant locale wiring', () => {
  for (const locale of ['en', 'zh'] as const) {
    describe(locale, () => {
      const t = makeT(locale)

      it('renders every tiered label and caption from the locale', () => {
        const view = resolvePricingVariant({ flat, tiers: TIERS }, t)
        expect(view.kind).toBe('tiered')
        // 3 bracket labels + caption.
        expect(renderedStrings(view)).toHaveLength(4)
      })

      it('renders every peak/valley label and caption from the locale', () => {
        const view = resolvePricingVariant({ flat, peakValley: PEAK_VALLEY }, t)
        expect(view.kind).toBe('peak_valley')
        // off-peak + peak labels, plus the caption.
        expect(renderedStrings(view)).toHaveLength(3)
      })

      it('interpolates the bracket bounds into the labels', () => {
        // The bound must actually land in the string: a label whose param was
        // renamed compiles fine and resolves fine while telling the user nothing.
        expect(formatTierLabel({ minTokens: 0, maxTokens: 32000 }, t)).toContain('32k')
        expect(formatTierLabel({ minTokens: 128000, maxTokens: null }, t)).toContain('128k')
        const range = formatTierLabel({ minTokens: 32000, maxTokens: 128000 }, t)
        expect(range).toContain('32k')
        expect(range).toContain('128k')
      })

      it('interpolates the peak windows, timezone and multiplier into the caption', () => {
        const { caption } = resolvePricingVariant({ flat, peakValley: PEAK_VALLEY }, t)
        expect(caption).toContain('09:00–12:00')
        expect(caption).toContain('14:00–18:00')
        expect(caption).toContain('Asia/Shanghai')
        expect(caption).toContain('2')
      })

      it('leaves no {placeholder} unsubstituted in any rendered string', () => {
        const strings = [
          ...renderedStrings(resolvePricingVariant({ flat, tiers: TIERS }, t)),
          ...renderedStrings(resolvePricingVariant({ flat, peakValley: PEAK_VALLEY }, t))
        ]
        for (const s of strings) {
          expect(s, `unsubstituted placeholder in: ${s}`).not.toMatch(/\{\w+\}/)
        }
      })

      it('defines exactly the keys the resolver asks for', () => {
        // Dead keys are how the two sides drifted unnoticed: the block looked
        // populated while none of it was reachable.
        const variant = (overlay[locale].pricing as Dict).variant as Dict
        expect(Object.keys(variant).sort()).toEqual(CALLED_KEYS)
      })
    })
  }

  it('uses different wording for en and zh captions', () => {
    // Guards a copy-paste of the en block into zh, which would ship English
    // pricing captions to Chinese users.
    const en = (overlay.en.pricing as Dict).variant as Dict
    const zh = (overlay.zh.pricing as Dict).variant as Dict
    expect(zh.tieredCaption).not.toBe(en.tieredCaption)
    expect(zh.offPeak).not.toBe(en.offPeak)
  })
})
