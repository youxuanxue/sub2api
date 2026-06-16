import { describe, it, expect } from 'vitest'
import {
  modalityHasTiers,
  pickModalityKey,
  resolveAvailableTiers,
  type ModalityKeyOption,
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
