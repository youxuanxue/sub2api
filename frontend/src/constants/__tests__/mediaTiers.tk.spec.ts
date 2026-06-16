import { describe, it, expect } from 'vitest'
import {
  modalityHasTiers,
  groupServes,
  pickModalityKey,
  resolveAvailableModels,
  defaultModelId,
  MEDIA_MODELS,
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
