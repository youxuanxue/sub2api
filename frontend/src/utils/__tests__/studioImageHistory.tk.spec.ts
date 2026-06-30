import { describe, expect, it } from 'vitest'
import {
  imageHistoryPromptTitle,
  isEphemeralImageSrc,
  matchImageHistoryModel,
  studioImageHistoryId,
} from '../studioImageHistory.tk'

describe('studioImageHistory.tk', () => {
  it('mints unique ids', () => {
    const a = studioImageHistoryId()
    const b = studioImageHistoryId()
    expect(a).toBeTruthy()
    expect(b).toBeTruthy()
    expect(a).not.toBe(b)
  })

  it('classifies ephemeral image src schemes', () => {
    expect(isEphemeralImageSrc('')).toBe(true)
    expect(isEphemeralImageSrc('data:image/png;base64,AA==')).toBe(true)
    expect(isEphemeralImageSrc('blob:http://localhost/x')).toBe(true)
    expect(isEphemeralImageSrc('https://cdn.example/a.png')).toBe(true)
    expect(isEphemeralImageSrc('file:///tmp/x.png')).toBe(false)
  })

  it('builds prompt tooltip with revised prompt when different', () => {
    const title = imageHistoryPromptTitle(
      { prompt: 'rabbit', revisedPrompt: 'a white rabbit in grass', model: 'imagen-4' },
      (text) => `revised: ${text}`
    )
    expect(title).toContain('rabbit')
    expect(title).toContain('revised: a white rabbit')
    expect(title).toContain('imagen-4')
  })

  it('matchImageHistoryModel resolves served or catalog id', () => {
    const models = [{ servedId: 'imagen-4.0-generate-001', model: { modelId: 'imagen-4-fast' } }]
    expect(matchImageHistoryModel(models, 'imagen-4.0-generate-001')?.model.modelId).toBe('imagen-4-fast')
    expect(matchImageHistoryModel(models, 'imagen-4-fast')?.servedId).toBe('imagen-4.0-generate-001')
  })
})
