import { describe, it, expect } from 'vitest'
import {
  modalityHasTiers,
  groupServes,
  pickModalityKey,
  resolveAvailableModels,
  defaultModelId,
  videoDurationDefault,
  VIDEO_DURATION_DEFAULT,
  MEDIA_MODELS,
  IMAGEN_IMAGE_SIZES,
  SEEDREAM_IMAGE_SIZES,
  GEMINI_IMAGE_SIZES,
  type ModalityKeyOption,
  type StudioParam,
  type MediaPriceMap,
} from '@/constants/mediaTiers.tk'
import { buildCatalogBillingIndex } from '@/utils/studioMediaCatalog.tk'

function catalogFromIds(entries: Array<[string, 'image' | 'video']>) {
  return buildCatalogBillingIndex(
    entries.map(([model_id, billing_mode]) => ({
      model_id,
      pricing: {
        billing_mode,
        output_cost_per_image: billing_mode === 'image' ? 0.01 : undefined,
        output_cost_per_second: billing_mode === 'video' ? 0.01 : undefined,
      },
    }))
  )
}

const IMAGEN_CATALOG = catalogFromIds([
  ['imagen-4.0-fast-generate-001', 'image'],
  ['imagen-4.0-generate-001', 'image'],
])
const VEO_CATALOG = catalogFromIds([['veo-3.1-generate-001', 'video']])
const GROK_VIDEO_CATALOG = catalogFromIds([['grok-imagine-video', 'video']])
const EMPTY_CATALOG = buildCatalogBillingIndex([])

// A Vertex group serves the imagen image tiers + the veo cinematic video tier.
const IMAGEN = new Set(['imagen-4.0-fast-generate-001', 'imagen-4.0-generate-001'])
const VEO = new Set(['veo-3.1-generate-001'])
const GROK_VIDEO = new Set(['grok-imagine-video'])
// An antigravity/gemini-text group serves none of the studio media tiers.
const ANTIGRAVITY = new Set(['claude-sonnet-4-5', 'gemini-3-flash'])

describe('modalityHasTiers', () => {
  it('is true when the pool backs at least one catalog media model', () => {
    expect(modalityHasTiers('image', IMAGEN, IMAGEN_CATALOG)).toBe(true)
    expect(modalityHasTiers('video', VEO, VEO_CATALOG)).toBe(true)
    expect(modalityHasTiers('video', GROK_VIDEO, GROK_VIDEO_CATALOG)).toBe(true)
  })

  it('is false for a pool with no catalog media model', () => {
    expect(modalityHasTiers('image', ANTIGRAVITY, IMAGEN_CATALOG)).toBe(false)
    expect(modalityHasTiers('video', ANTIGRAVITY, VEO_CATALOG)).toBe(false)
    expect(modalityHasTiers('image', VEO, IMAGEN_CATALOG)).toBe(false)
    expect(modalityHasTiers('video', IMAGEN, VEO_CATALOG)).toBe(false)
    expect(modalityHasTiers('image', new Set(), EMPTY_CATALOG)).toBe(false)
  })

  it('is price-agnostic (the picker must not need a per-group price fetch)', () => {
    expect(modalityHasTiers('image', IMAGEN, IMAGEN_CATALOG)).toBe(true)
    expect(resolveAvailableModels('image', IMAGEN, new Map())).toEqual([])
  })
})

describe('groupServes (chat as a peer picker modality)', () => {
  it('serves chat when the pool exposes any chat-classified id', () => {
    expect(groupServes('chat', ANTIGRAVITY, EMPTY_CATALOG)).toBe(true)
    expect(groupServes('chat', new Set(['gpt-5']), EMPTY_CATALOG)).toBe(true)
  })

  it('does NOT serve chat for an image/video-only pool', () => {
    expect(groupServes('chat', IMAGEN, IMAGEN_CATALOG)).toBe(false)
    expect(groupServes('chat', VEO, VEO_CATALOG)).toBe(false)
    expect(groupServes('chat', new Set(), EMPTY_CATALOG)).toBe(false)
  })

  it('delegates to catalog billing for image/video', () => {
    expect(groupServes('image', IMAGEN, IMAGEN_CATALOG)).toBe(true)
    expect(groupServes('video', VEO, VEO_CATALOG)).toBe(true)
    expect(groupServes('image', ANTIGRAVITY, IMAGEN_CATALOG)).toBe(false)
  })
})

function opt(id: number, isTrial: boolean, availableIds: Set<string>): ModalityKeyOption {
  return { id, isTrial, availableIds }
}

describe('pickModalityKey', () => {
  it('returns currentId unchanged when no options', () => {
    expect(pickModalityKey([], 'image', 7, EMPTY_CATALOG)).toBe(7)
    expect(pickModalityKey([], 'image', null, EMPTY_CATALOG)).toBe(null)
  })

  it('keeps the current key when it already serves the modality', () => {
    const opts = [opt(1, false, ANTIGRAVITY), opt(2, false, IMAGEN)]
    expect(pickModalityKey(opts, 'image', 2, IMAGEN_CATALOG)).toBe(2)
  })

  it('moves off a non-serving current key to a serving one', () => {
    const opts = [opt(1, true, ANTIGRAVITY), opt(2, false, IMAGEN)]
    expect(pickModalityKey(opts, 'image', 1, IMAGEN_CATALOG)).toBe(2)
  })

  it('prefers a trial-named serving key over the first serving key', () => {
    const opts = [opt(1, false, IMAGEN), opt(2, true, IMAGEN)]
    expect(pickModalityKey(opts, 'image', null, IMAGEN_CATALOG)).toBe(2)
  })

  it('re-targets per modality (image vs video live on different groups)', () => {
    const opts = [opt(1, false, IMAGEN), opt(2, false, VEO)]
    expect(pickModalityKey(opts, 'image', null, IMAGEN_CATALOG)).toBe(1)
    expect(pickModalityKey(opts, 'video', null, VEO_CATALOG)).toBe(2)
  })

  it('lands a chat key on a chat-serving group, off a media-only current key', () => {
    const opts = [opt(1, false, IMAGEN), opt(2, true, ANTIGRAVITY)]
    expect(pickModalityKey(opts, 'chat', 1, IMAGEN_CATALOG)).toBe(2)
    expect(pickModalityKey(opts, 'chat', 2, EMPTY_CATALOG)).toBe(2)
  })

  it('falls back to the seed/global default when nothing serves the modality', () => {
    const opts = [opt(1, false, ANTIGRAVITY), opt(2, true, ANTIGRAVITY)]
    expect(pickModalityKey(opts, 'image', 1, IMAGEN_CATALOG)).toBe(1)
    expect(pickModalityKey(opts, 'image', null, IMAGEN_CATALOG)).toBe(2)
  })
})

describe('resolveAvailableModels (transparent model picker)', () => {
  const IMAGEN3 = new Set([
    'imagen-4.0-fast-generate-001',
    'imagen-4.0-generate-001',
    'imagen-4.0-ultra-generate-001',
  ])
  // Live prices come from the per-user catalog (getMePricingCatalog), not hardcoded.
  const IMAGEN_PRICES: MediaPriceMap = new Map([
    ['imagen-4.0-fast-generate-001', { perImage: 0.02, billingMode: 'image' }],
    ['imagen-4.0-generate-001', { perImage: 0.04, billingMode: 'image' }],
    ['imagen-4.0-ultra-generate-001', { perImage: 0.06, billingMode: 'image' }],
  ])

  it('lists only priced+servable image models, sorted cheap → premium, with live price', () => {
    const out = resolveAvailableModels('image', IMAGEN3, IMAGEN_PRICES)
    expect(out.map((r) => r.model.modelId)).toEqual([
      'imagen-4.0-fast-generate-001', // 0.02
      'imagen-4.0-generate-001', // 0.04
      'imagen-4.0-ultra-generate-001', // 0.06
    ])
    expect(out.map((r) => r.baseImagePrice)).toEqual([0.02, 0.04, 0.06])
    expect(out.every((r) => r.servedId === r.model.modelId)).toBe(true)
  })

  it('hides a servable model that has no live price (priced ∩ servable)', () => {
    const priceless = new Map() // served but unpriced
    expect(resolveAvailableModels('image', IMAGEN3, priceless)).toEqual([])
  })

  it('resolves a model + price via an alias id and reports the served id', () => {
    const out = resolveAvailableModels(
      'image',
      new Set(['doubao-seedream-4-0-250828']),
      new Map([['doubao-seedream-4-0-250828', { perImage: 0.03, billingMode: 'image' }]])
    )
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe('seedream-4-0-250828')
    expect(out[0].servedId).toBe('doubao-seedream-4-0-250828')
    expect(out[0].baseImagePrice).toBe(0.03)
  })

  it('surfaces newly probed Seedream image models with curated presentation', () => {
    const out = resolveAvailableModels(
      'image',
      new Set(['doubao-seedream-4-5-251128', 'doubao-seedream-5-0-260128']),
      new Map([
        ['doubao-seedream-4-5-251128', { perImage: 0.0373, billingMode: 'image' }],
        ['doubao-seedream-5-0-260128', { perImage: 0.0328, billingMode: 'image' }],
      ])
    )
    expect(out.map((r) => r.model.modelId)).toEqual([
      'doubao-seedream-5-0-260128',
      'doubao-seedream-4-5-251128',
    ])
    expect(out.map((r) => r.model.displayName)).toEqual(['Seedream 5.0', 'Seedream 4.5'])
    expect(out.every((r) => r.model.imageSizes === SEEDREAM_IMAGE_SIZES)).toBe(true)
  })

  it('uses the routed doubao Seedance id as canonical and treats no-prefix as an alias', () => {
    const out = resolveAvailableModels(
      'video',
      new Set(['doubao-seedance-1-0-pro-250528']),
      new Map([['doubao-seedance-1-0-pro-250528', { perSecond: 0.1088, billingMode: 'video' }]])
    )
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe('doubao-seedance-1-0-pro-250528')
    expect(out[0].servedId).toBe('doubao-seedance-1-0-pro-250528')
    expect(out[0].model.aliasIds).toContain('seedance-1-0-pro-250528')
  })

  it('surfaces Seedance Pro Fast with the conservative probed duration', () => {
    const out = resolveAvailableModels(
      'video',
      new Set(['doubao-seedance-1-0-pro-fast-251015']),
      new Map([['doubao-seedance-1-0-pro-fast-251015', { perSecond: 0.0305, billingMode: 'video' }]])
    )
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe('doubao-seedance-1-0-pro-fast-251015')
    expect(out[0].model.displayName).toBe('Seedance 1.0 · Pro Fast')
    expect(out[0].model.videoDurations).toEqual([5])
  })

  it('surfaces grok-imagine-video when priced and in the group pool', () => {
    const out = resolveAvailableModels(
      'video',
      GROK_VIDEO,
      new Map([['grok-imagine-video', { perSecond: 0.08, billingMode: 'video', vendor: 'xai' }]])
    )
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe('grok-imagine-video')
    expect(out[0].model.displayName).toBe('Grok Imagine · Video')
    expect(out[0].perSecond).toBe(0.08)
  })

  it('builds conservative defaults when catalog lists a model not in MEDIA_MODELS', () => {
    const unknownId = 'future-video-model-xyz'
    const out = resolveAvailableModels(
      'video',
      new Set([unknownId]),
      new Map([[unknownId, { perSecond: 0.12, billingMode: 'video', vendor: 'xai' }]])
    )
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe(unknownId)
    expect(out[0].model.displayName).toBe('Future Video Model Xyz')
    expect(out[0].model.vendorLabel).toBe('xAI')
    expect(out[0].model.supportedParams).toEqual([])
    expect(out[0].model.videoDurations).toEqual([8])
    expect(out[0].perSecond).toBe(0.12)
  })

  it('excludes models not in the group pool', () => {
    expect(resolveAvailableModels('image', new Set(['gemini-3-flash']), new Map())).toEqual([])
    expect(
      resolveAvailableModels('video', new Set(['imagen-4.0-ultra-generate-001']), IMAGEN_PRICES)
    ).toEqual([])
  })

  it('video pool surfaces only served+priced video models with per-second price', () => {
    const out = resolveAvailableModels(
      'video',
      new Set(['veo-3.1-generate-001']),
      new Map([['veo-3.1-generate-001', { perSecond: 0.4, billingMode: 'video' }]])
    )
    expect(out.map((r) => r.model.modelId)).toEqual(['veo-3.1-generate-001'])
    expect(out[0].perSecond).toBe(0.4)
  })

  it('skips ids whose billing_mode does not match the requested modality', () => {
    expect(
      resolveAvailableModels(
        'video',
        new Set(['imagen-4.0-ultra-generate-001']),
        new Map([['imagen-4.0-ultra-generate-001', { perImage: 0.06, billingMode: 'image' }]])
      )
    ).toEqual([])
  })
})

describe('defaultModelId', () => {
  const PRICES: MediaPriceMap = new Map([
    ['imagen-4.0-fast-generate-001', { perImage: 0.02, billingMode: 'image' }],
    ['imagen-4.0-generate-001', { perImage: 0.04, billingMode: 'image' }],
  ])
  it('picks the cheapest non-footgun model', () => {
    const out = resolveAvailableModels(
      'image',
      new Set(['imagen-4.0-generate-001', 'imagen-4.0-fast-generate-001']),
      PRICES
    )
    expect(defaultModelId(out)).toBe('imagen-4.0-fast-generate-001')
  })
  it('returns null when nothing is servable', () => {
    expect(defaultModelId([])).toBe(null)
  })
  it('never auto-selects a needsApikeyAccount model', () => {
    // No shipped model is flagged today; assert the invariant holds for the catalog.
    expect(MEDIA_MODELS.some((m) => m.needsApikeyAccount)).toBe(false)
  })
})

describe('capability map honesty (verified against new-api adaptors)', () => {
  // Only params an adaptor ACTUALLY reads are listed; fps was removed (no adaptor
  // honors it), imagen/seedream honor none, seedance drops negative_prompt.
  const VALID: StudioParam[] = ['negativePrompt', 'seed', 'firstFrameImage', 'generateAudio']
  const byId = (id: string) => MEDIA_MODELS.find((m) => m.modelId === id)!

  it('every supportedParams entry is a valid StudioParam', () => {
    for (const m of MEDIA_MODELS) {
      for (const p of m.supportedParams) expect(VALID).toContain(p)
    }
  })
  it('image models (imagen/seedream) honor no advanced params', () => {
    for (const m of MEDIA_MODELS.filter((m) => m.modality === 'image')) {
      expect(m.supportedParams).toEqual([])
    }
  })
  it('veo honors negativePrompt + seed + firstFrameImage', () => {
    expect(byId('veo-3.1-generate-001').supportedParams).toEqual(['negativePrompt', 'seed', 'firstFrameImage', 'generateAudio'])
  })
  it('seedance honors seed + firstFrameImage but NOT negativePrompt (adaptor drops it)', () => {
    const s = byId('doubao-seedance-1-0-pro-250528').supportedParams
    expect(s).toContain('seed')
    expect(s).toContain('firstFrameImage')
    expect(s).not.toContain('negativePrompt')
  })
})

describe('video durations (per-model discrete, never a footgun)', () => {
  // Regression guard for the "$31.80 for a 53s Veo clip" footgun: the duration
  // axis used to be a global 1–60s slider, so users were quoted (and pre-charged)
  // for durations the upstream always rejects. Each video model now declares its
  // accepted discrete seconds; this locks that invariant.
  const videoModels = MEDIA_MODELS.filter((m) => m.modality === 'video')
  const byId = (id: string) => MEDIA_MODELS.find((m) => m.modelId === id)!

  it('every video model declares a non-empty videoDurations; image models declare none', () => {
    expect(videoModels.length).toBeGreaterThan(0)
    for (const m of videoModels) {
      expect(m.videoDurations && m.videoDurations.length).toBeTruthy()
    }
    for (const m of MEDIA_MODELS.filter((m) => m.modality === 'image')) {
      expect(m.videoDurations).toBeUndefined()
    }
  })

  it('all declared durations are positive integers within a sane [1,15] ceiling (kills the >15s footgun)', () => {
    for (const m of videoModels) {
      for (const d of m.videoDurations!) {
        expect(Number.isInteger(d)).toBe(true)
        expect(d).toBeGreaterThanOrEqual(1)
        expect(d).toBeLessThanOrEqual(15)
      }
    }
  })

  it('the default duration is the MAX accepted value (user directive) and is itself accepted', () => {
    for (const m of videoModels) {
      const def = videoDurationDefault(m.videoDurations)
      expect(def).toBe(Math.max(...m.videoDurations!))
      expect(m.videoDurations).toContain(def)
    }
  })

  it('videoDurationDefault falls back to VIDEO_DURATION_DEFAULT when no durations are declared', () => {
    expect(videoDurationDefault(undefined)).toBe(VIDEO_DURATION_DEFAULT)
    expect(videoDurationDefault([])).toBe(VIDEO_DURATION_DEFAULT)
  })

  it('Veo 3.1 accepts exactly 4/6/8s; Seedance uses documented discrete seconds; Grok Imagine 5s', () => {
    expect(byId('veo-3.1-generate-001').videoDurations).toEqual([4, 6, 8])
    expect(byId('veo-3.1-fast-generate-001')).toBeUndefined()
    expect(byId('doubao-seedance-1-0-pro-250528').videoDurations).toEqual([5, 10])
    expect(byId('doubao-seedance-1-0-pro-fast-251015').videoDurations).toEqual([5])
    expect(byId('doubao-seedance-1-5-pro-251215').videoDurations).toEqual([5])
    expect(byId('doubao-seedance-2-0-260128').videoDurations).toEqual([5])
    expect(byId('doubao-seedance-2-0-fast-260128').videoDurations).toEqual([4, 8, 12])
    expect(byId('grok-imagine-video').videoDurations).toEqual([5])
  })
})

describe('image aspect options (per-model, upstream-valid wire values)', () => {
  // Imagen's documented aspectRatio set. The reported bug was the studio sending
  // 1536x1024 / 1024x1536 → the adaptor maps those to 3:2 / 2:3, which Imagen
  // hard-400s ("Invalid aspect ratio, 3:2"). So NO image model may offer 3:2/2:3,
  // and Imagen must send the ratio code verbatim.
  const IMAGEN_VALID = new Set(['1:1', '3:4', '4:3', '9:16', '16:9'])

  it('image models with flatPricePerImage may omit imageSizes (grok-imagine)', () => {
    const flatNoSizes = MEDIA_MODELS.filter((m) => m.modality === 'image' && m.flatPricePerImage && !m.imageSizes)
    expect(flatNoSizes.some((m) => m.modelId.startsWith('grok-imagine-'))).toBe(true)
  })

  it('other image models carry imageSizes; video carry none', () => {
    // Gemini-native image regained its aspect picker: a prod canary (2026-06-17) confirmed
    // cloudcode-pa honors imageConfig.aspectRatio for all 10 documented ratios, lifting the
    // #807 R-001 "no picker" deferral. So every image model now carries imageSizes; only
    // video models (passthrough hint, separate VIDEO_ASPECT_PRESETS) carry none.
    for (const m of MEDIA_MODELS.filter((m) => m.modality === 'image' && !m.flatPricePerImage)) {
      expect(m.imageSizes && m.imageSizes.length).toBeTruthy()
    }
    for (const m of MEDIA_MODELS.filter((m) => m.modality === 'video')) {
      expect(m.imageSizes).toBeUndefined()
    }
  })

  it('IMAGEN models never offer an Imagen-invalid ratio (regression: 3:2 / 2:3)', () => {
    // The 3:2/2:3 rule is IMAGEN-specific: those map to a hard-400 on Vertex.
    // Gemini legitimately supports 3:2/2:3/21:9 (different upstream), so the guard
    // is scoped to imagen models (the ones whose imageSizes is IMAGEN_IMAGE_SIZES).
    const imagenModels = MEDIA_MODELS.filter((m) => m.imageSizes === IMAGEN_IMAGE_SIZES)
    expect(imagenModels.length).toBeGreaterThan(0)
    for (const m of imagenModels) {
      for (const opt of m.imageSizes ?? []) {
        expect(opt.ratio).not.toBe('3:2')
        expect(opt.ratio).not.toBe('2:3')
        expect(IMAGEN_VALID.has(opt.ratio)).toBe(true)
      }
    }
  })

  it('Imagen sends the ratio code verbatim on the wire (value === ratio)', () => {
    for (const opt of IMAGEN_IMAGE_SIZES) {
      expect(opt.value).toBe(opt.ratio)
      expect(IMAGEN_VALID.has(opt.value)).toBe(true)
    }
    expect(new Set(IMAGEN_IMAGE_SIZES.map((o) => o.ratio))).toEqual(IMAGEN_VALID)
  })

  it('gemini is flatImageBilling; imagen is flat-priced (no size tier); only seedream stays tiered', () => {
    const gemini = MEDIA_MODELS.filter((m) => m.modality === 'image' && m.flatImageBilling)
    expect(gemini.length).toBeGreaterThan(0)
    for (const m of gemini) {
      // flat billing (no 1K/2K/4K size tier) is orthogonal to aspect ratio: the picker
      // now drives aspect_ratio only, billing stays flat per image.
      expect(m.imageSizes).toBe(GEMINI_IMAGE_SIZES)
    }
    // Imagen bills Google's flat official $/image → flatPricePerImage, but NOT
    // flatImageBilling (it keeps /v1/images routing, multi-image n, no image-input).
    // Mirrors backend tkIsFlatPerImageModel.
    for (const m of MEDIA_MODELS.filter((m) => m.imageSizes === IMAGEN_IMAGE_SIZES)) {
      expect(m.flatPricePerImage).toBe(true)
      expect(m.flatImageBilling).toBeFalsy()
    }
    // Seedream sends real pixel sizes → keeps the 1K/2K/4K size-tier multiplier:
    // neither flat flag set.
    for (const m of MEDIA_MODELS.filter((m) => m.imageSizes === SEEDREAM_IMAGE_SIZES)) {
      expect(m.flatPricePerImage).toBeFalsy()
      expect(m.flatImageBilling).toBeFalsy()
    }
  })

  it('GEMINI_IMAGE_SIZES sends the ratio code verbatim and is exactly the 10 prod-verified ratios', () => {
    // Mirrors the prod canary (2026-06-17): each of these returned dims matching the
    // requested ratio. value === ratio because gemini bills flat (no pixel size).
    const PROD_VERIFIED = new Set(['1:1', '2:3', '3:2', '3:4', '4:3', '4:5', '5:4', '9:16', '16:9', '21:9'])
    for (const opt of GEMINI_IMAGE_SIZES) {
      expect(opt.value).toBe(opt.ratio)
    }
    expect(new Set(GEMINI_IMAGE_SIZES.map((o) => o.ratio))).toEqual(PROD_VERIFIED)
  })

  it('Seedream sends pixel WxH within ARK range [1024², 4096²], ratio range [1/16,16]', () => {
    for (const opt of SEEDREAM_IMAGE_SIZES) {
      const m = /^(\d+)x(\d+)$/.exec(opt.value)
      expect(m).not.toBeNull() // pixels, never a ratio string
      const [w, h] = [Number(m![1]), Number(m![2])]
      const total = w * h
      expect(total).toBeGreaterThanOrEqual(1024 * 1024)
      expect(total).toBeLessThanOrEqual(4096 * 4096)
      const ratio = w / h
      expect(ratio).toBeGreaterThanOrEqual(1 / 16)
      expect(ratio).toBeLessThanOrEqual(16)
      // maxEdge ≤ 2048 keeps every Seedream option in the 2K billing tier.
      expect(Math.max(w, h)).toBeLessThanOrEqual(2048)
    }
  })
})
