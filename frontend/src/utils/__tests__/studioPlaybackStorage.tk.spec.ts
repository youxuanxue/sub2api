import { describe, expect, it } from 'vitest'
import { classifyVideoUrlStorage } from '../studioPlaybackStorage.tk'

describe('classifyVideoUrlStorage', () => {
  it('marks inline data:video as local-cacheable', () => {
    expect(classifyVideoUrlStorage('data:video/mp4;base64,AAAA')).toBe('inline-local')
  })

  it('marks empty url as expired', () => {
    expect(classifyVideoUrlStorage('')).toBe('expired')
  })

  it('marks http url as unknown until probed', () => {
    expect(classifyVideoUrlStorage('https://cdn.example/v.mp4')).toBe('unknown')
  })
})
