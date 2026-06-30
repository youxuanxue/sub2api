import { describe, expect, it, vi } from 'vitest'
import { classifyVideoUrlStorage, tagStudioVideoPlayback, videoTaskPlaybackStorageKind } from '../studioPlaybackStorage.tk'

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

describe('videoTaskPlaybackStorageKind', () => {
  it('derives expired when url is stripped', () => {
    expect(videoTaskPlaybackStorageKind({ url: '', urlExpired: true })).toBe('expired')
  })

  it('prefers persisted playbackStorage', () => {
    expect(videoTaskPlaybackStorageKind({ url: 'https://x/v.mp4', playbackStorage: 'inline-local' })).toBe('inline-local')
  })
})

describe('tagStudioVideoPlayback', () => {
  it('marks empty url as expired storage', async () => {
    const patchVideoTask = vi.fn()
    await tagStudioVideoPlayback(
      { patchVideoTask, cacheInlineMedia: vi.fn(async () => undefined) },
      'vt_empty',
      ''
    )
    expect(patchVideoTask).toHaveBeenCalledWith('vt_empty', { playbackStorage: 'expired' })
  })
})
