import { describe, it, expect } from 'vitest'
import {
  modalityHasTiers,
  pickModalityKey,
  resolveAvailableTiers,
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

  it('agrees with resolveAvailableTiers', () => {
    expect(modalityHasTiers('image', IMAGEN)).toBe(resolveAvailableTiers('image', IMAGEN).length > 0)
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

  it('lists only servable image models, sorted cheap → premium', () => {
    const out = resolveAvailableModels('image', IMAGEN3)
    expect(out.map((r) => r.model.modelId)).toEqual([
      'imagen-4.0-fast-generate-001', // 0.02
      'imagen-4.0-generate-001', // 0.04
      'imagen-4.0-ultra-generate-001', // 0.06
    ])
    expect(out.every((r) => r.servedId === r.model.modelId)).toBe(true)
  })

  it('resolves a model via an alias id and reports the served id', () => {
    const out = resolveAvailableModels('image', new Set(['doubao-seedream-4-0-250828']))
    expect(out).toHaveLength(1)
    expect(out[0].model.modelId).toBe('seedream-4-0-250828')
    expect(out[0].servedId).toBe('doubao-seedream-4-0-250828') // billing key = the served id
  })

  it('excludes models not in the group pool (priced∩servable)', () => {
    expect(resolveAvailableModels('image', new Set(['gemini-3-flash']))).toEqual([])
    expect(resolveAvailableModels('video', new Set(['imagen-4.0-ultra-generate-001']))).toEqual([])
  })

  it('video pool surfaces only the served video models', () => {
    const out = resolveAvailableModels('video', new Set(['veo-3.1-generate-001']))
    expect(out.map((r) => r.model.modelId)).toEqual(['veo-3.1-generate-001'])
  })
})

describe('defaultModelId', () => {
  it('picks the cheapest non-footgun model', () => {
    const out = resolveAvailableModels('image', new Set(['imagen-4.0-generate-001', 'imagen-4.0-fast-generate-001']))
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

describe('capability map honesty', () => {
  const VALID: StudioParam[] = ['quality', 'style', 'negativePrompt', 'seed', 'firstFrameImage', 'fps', 'resolution']
  it('every supportedParams entry is a valid StudioParam', () => {
    for (const m of MEDIA_MODELS) {
      for (const p of m.supportedParams) expect(VALID).toContain(p)
    }
  })
  it('no shipped model claims quality/style/resolution (none take effect today)', () => {
    for (const m of MEDIA_MODELS) {
      expect(m.supportedParams).not.toContain('quality')
      expect(m.supportedParams).not.toContain('style')
      expect(m.supportedParams).not.toContain('resolution')
    }
  })
  it('veo omits seed/fps (Vertex video path does not honor them)', () => {
    const veo = MEDIA_MODELS.filter((m) => m.modelId.startsWith('veo-'))
    expect(veo.length).toBeGreaterThan(0)
    for (const m of veo) {
      expect(m.supportedParams).not.toContain('seed')
      expect(m.supportedParams).not.toContain('fps')
    }
  })
  it('image models never expose video-only params', () => {
    for (const m of MEDIA_MODELS.filter((m) => m.modality === 'image')) {
      expect(m.supportedParams).not.toContain('fps')
      expect(m.supportedParams).not.toContain('firstFrameImage')
    }
  })
})
