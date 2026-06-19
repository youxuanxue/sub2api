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
} from '@/constants/mediaTiers.tk'

// A Vertex group serves the imagen image tiers + the veo cinematic video tier.
const IMAGEN = new Set(['imagen-4.0-fast-generate-001', 'imagen-4.0-generate-001'])
const VEO = new Set(['veo-3.1-generate-001'])
// An antigravity/gemini-text group serves none of the studio media tiers.
const ANTIGRAVITY = new Set(['claude-sonnet-4-5', 'gemini-3-flash'])

describe('modalityHasTiers', () => {
  it('is true when the pool backs at least one tier', () => {
    expect(modalityHasTiers('image', IMAGEN)).toBe(true)
    expect(modalityHasTiers('video', VEO)).toBe(true)
  })

  it('is false for a pool with no backing model', () => {
    expect(modalityHasTiers('image', ANTIGRAVITY)).toBe(false)
    expect(modalityHasTiers('video', ANTIGRAVITY)).toBe(false)
    expect(modalityHasTiers('image', VEO)).toBe(false) // veo backs video only
    expect(modalityHasTiers('video', IMAGEN)).toBe(false)
    expect(modalityHasTiers('image', new Set())).toBe(false)
  })

  it('is price-agnostic (the picker must not need a per-group price fetch)', () => {
    // True on a servable group even before prices load — unlike the priced card
    // resolver, which (deliberately) returns [] without a price map.
    expect(modalityHasTiers('image', IMAGEN)).toBe(true)
    expect(resolveAvailableModels('image', IMAGEN, new Map())).toEqual([])
  })
})

describe('groupServes (chat as a peer picker modality)', () => {
  it('serves chat when the pool exposes any chat-classified id', () => {
    // modalityForModel classifies non image/video ids as chat.
    expect(groupServes('chat', ANTIGRAVITY)).toBe(true) // claude/gemini text models
    expect(groupServes('chat', new Set(['gpt-5']))).toBe(true)
  })

  it('does NOT serve chat for an image/video-only pool', () => {
    expect(groupServes('chat', IMAGEN)).toBe(false)
    expect(groupServes('chat', VEO)).toBe(false)
    expect(groupServes('chat', new Set())).toBe(false)
  })

  it('delegates to media tiers for image/video', () => {
    expect(groupServes('image', IMAGEN)).toBe(true)
    expect(groupServes('video', VEO)).toBe(true)
    expect(groupServes('image', ANTIGRAVITY)).toBe(false)
  })
})

function opt(id: number, isTrial: boolean, availableIds: Set<string>): ModalityKeyOption {
  return { id, isTrial, availableIds }
}

describe('pickModalityKey', () => {
  it('returns currentId unchanged when no options', () => {
    expect(pickModalityKey([], 'image', 7)).toBe(7)
    expect(pickModalityKey([], 'image', null)).toBe(null)
  })

  it('keeps the current key when it already serves the modality', () => {
    const opts = [opt(1, false, ANTIGRAVITY), opt(2, false, IMAGEN)]
    expect(pickModalityKey(opts, 'image', 2)).toBe(2)
  })

  it('moves off a non-serving current key to a serving one', () => {
    // The prod regression: bootstrap seeded the antigravity key (1); image needs
    // the imagen group (2).
    const opts = [opt(1, true, ANTIGRAVITY), opt(2, false, IMAGEN)]
    expect(pickModalityKey(opts, 'image', 1)).toBe(2)
  })

  it('prefers a trial-named serving key over the first serving key', () => {
    const opts = [opt(1, false, IMAGEN), opt(2, true, IMAGEN)]
    expect(pickModalityKey(opts, 'image', null)).toBe(2)
  })

  it('re-targets per modality (image vs video live on different groups)', () => {
    const opts = [opt(1, false, IMAGEN), opt(2, false, VEO)]
    expect(pickModalityKey(opts, 'image', null)).toBe(1)
    expect(pickModalityKey(opts, 'video', null)).toBe(2)
  })

  it('lands a chat key on a chat-serving group, off a media-only current key', () => {
    // Studio now defaults to the chat tab: a media-only key must yield to a
    // chat-serving one (the inverse of the imagen regression above).
    const opts = [opt(1, false, IMAGEN), opt(2, true, ANTIGRAVITY)]
    expect(pickModalityKey(opts, 'chat', 1)).toBe(2)
    // keep a chat-serving current key untouched
    expect(pickModalityKey(opts, 'chat', 2)).toBe(2)
  })

  it('falls back to the seed/global default when nothing serves the modality', () => {
    const opts = [opt(1, false, ANTIGRAVITY), opt(2, true, ANTIGRAVITY)]
    // current seed kept so the UI still shows an honest empty state
    expect(pickModalityKey(opts, 'image', 1)).toBe(1)
    // no seed → trial-named key, else first
    expect(pickModalityKey(opts, 'image', null)).toBe(2)
  })
})

describe('resolveAvailableModels (transparent model picker)', () => {
  const IMAGEN3 = new Set([
    'imagen-4.0-fast-generate-001',
    'imagen-4.0-generate-001',
    'imagen-4.0-ultra-generate-001',
  ])
  // Live prices come from the per-user catalog (getMePricingCatalog), not hardcoded.
  const IMAGEN_PRICES = new Map([
    ['imagen-4.0-fast-generate-001', { perImage: 0.02 }],
    ['imagen-4.0-generate-001', { perImage: 0.04 }],
    ['imagen-4.0-ultra-generate-001', { perImage: 0.06 }],
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
      new Map([['doubao-seedream-4-0-250828', { perImage: 0.03 }]])
    )
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe('seedream-4-0-250828')
    expect(out[0].servedId).toBe('doubao-seedream-4-0-250828') // billing key = the served id
    expect(out[0].baseImagePrice).toBe(0.03)
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
      new Map([['veo-3.1-generate-001', { perSecond: 0.4 }]])
    )
    expect(out.map((r) => r.model.modelId)).toEqual(['veo-3.1-generate-001'])
    expect(out[0].perSecond).toBe(0.4)
  })
})

describe('defaultModelId', () => {
  const PRICES = new Map([
    ['imagen-4.0-fast-generate-001', { perImage: 0.02 }],
    ['imagen-4.0-generate-001', { perImage: 0.04 }],
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
  const VALID: StudioParam[] = ['negativePrompt', 'seed', 'firstFrameImage']
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
    expect(byId('veo-3.1-generate-001').supportedParams).toEqual(['negativePrompt', 'seed', 'firstFrameImage'])
  })
  it('seedance honors seed + firstFrameImage but NOT negativePrompt (adaptor drops it)', () => {
    const s = byId('seedance-1-0-pro-250528').supportedParams
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

  it('Veo 3.1 accepts exactly 4/6/8s (Vertex official); Seedance 1.0 exactly 5/10s', () => {
    expect(byId('veo-3.1-generate-001').videoDurations).toEqual([4, 6, 8])
    expect(byId('veo-3.1-fast-generate-001').videoDurations).toEqual([4, 6, 8])
    expect(byId('seedance-1-0-pro-250528').videoDurations).toEqual([5, 10])
  })
})

describe('image aspect options (per-model, upstream-valid wire values)', () => {
  // Imagen's documented aspectRatio set. The reported bug was the studio sending
  // 1536x1024 / 1024x1536 → the adaptor maps those to 3:2 / 2:3, which Imagen
  // hard-400s ("Invalid aspect ratio, 3:2"). So NO image model may offer 3:2/2:3,
  // and Imagen must send the ratio code verbatim.
  const IMAGEN_VALID = new Set(['1:1', '3:4', '4:3', '9:16', '16:9'])

  it('all image models carry imageSizes (gemini too, post-canary); video carry none', () => {
    // Gemini-native image regained its aspect picker: a prod canary (2026-06-17) confirmed
    // cloudcode-pa honors imageConfig.aspectRatio for all 10 documented ratios, lifting the
    // #807 R-001 "no picker" deferral. So every image model now carries imageSizes; only
    // video models (passthrough hint, separate VIDEO_ASPECT_PRESETS) carry none.
    for (const m of MEDIA_MODELS.filter((m) => m.modality === 'image')) {
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
