import { afterEach, describe, expect, it, vi } from 'vitest'
import { videoPlaybackUrl } from '../studioMedia.tk'

// jsdom does not implement URL.createObjectURL / revokeObjectURL, so we define them
// per-test and restore after. (videoPlaybackUrl falls back to the original src when
// they are absent — exercised implicitly by the non-mocked cases.)
const urlStatic = URL as unknown as {
  createObjectURL?: (b: Blob) => string
  revokeObjectURL?: (u: string) => void
}

describe('videoPlaybackUrl', () => {
  afterEach(() => {
    delete urlStatic.createObjectURL
    delete urlStatic.revokeObjectURL
    vi.restoreAllMocks()
  })

  it('returns empty + noop for an empty src', () => {
    const { url, revoke } = videoPlaybackUrl('')
    expect(url).toBe('')
    expect(() => revoke()).not.toThrow()
  })

  it('hands an http(s) URL straight to the browser (no Blob, noop revoke)', () => {
    const revokeFn = vi.fn()
    urlStatic.revokeObjectURL = revokeFn
    const { url, revoke } = videoPlaybackUrl('https://cdn.example/v.mp4')
    expect(url).toBe('https://cdn.example/v.mp4')
    revoke()
    expect(revokeFn).not.toHaveBeenCalled() // http URL has no Blob to free
  })

  it('converts inline data:video to a tab-local Blob URL and revokes it', () => {
    const create = vi.fn(() => 'blob:mock-123')
    const revokeFn = vi.fn()
    urlStatic.createObjectURL = create
    urlStatic.revokeObjectURL = revokeFn
    const { url, revoke } = videoPlaybackUrl('data:video/mp4;base64,AAAA')
    expect(url).toBe('blob:mock-123')
    expect(create).toHaveBeenCalledOnce()
    revoke()
    expect(revokeFn).toHaveBeenCalledWith('blob:mock-123')
  })

  it('falls back to the original src for a non-base64 data: URI', () => {
    const { url } = videoPlaybackUrl('data:video/mp4,notbase64')
    expect(url).toBe('data:video/mp4,notbase64')
  })
})
