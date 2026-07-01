import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  groupImageHistoryByTs,
  groupVideoHistoryByBatch,
  imageHistoryItemAvailable,
  shouldShowStudioSaveReminder,
  videoPlaybackUrl,
  videoPlaybackUrlAsync,
  videoTaskCardPresentation,
  videoTaskPlaybackAvailable,
  videoCopyLinkAvailable,
  videoTaskCopyLinkAvailable,
} from '../studioMedia.tk'
import type { ImageHistoryItem, VideoTaskItem } from '@/composables/useMediaLibrary'

// jsdom does not implement URL.createObjectURL / revokeObjectURL, so we define them
// per-test and restore after. (videoPlaybackUrl falls back to the original src when
// they are absent — exercised implicitly by the non-mocked cases.)
const urlStatic = URL as unknown as {
  createObjectURL?: (b: Blob) => string
  revokeObjectURL?: (u: string) => void
}

describe('videoTaskCardPresentation', () => {
  it('is expired when reload stripped the upstream url', () => {
    expect(
      videoTaskCardPresentation({
        state: 'succeeded',
        url: '',
        urlExpired: true,
      })
    ).toBe('expired')
  })

  it('is loading for http upstream until CORS classification finishes', () => {
    expect(
      videoTaskCardPresentation({
        state: 'succeeded',
        url: 'https://cdn.example/fresh.mp4',
        urlExpired: false,
      })
    ).toBe('loading')
  })

  it('is download-only when upstream CORS blocks inline preview/cache', () => {
    expect(
      videoTaskCardPresentation({
        state: 'succeeded',
        url: 'https://cdn.example/seedance.mp4',
        urlExpired: false,
        playbackStorage: 'upstream-cors-blocked',
      })
    ).toBe('download-only')
  })

  it('is inline-play when upstream CORS probe succeeds', () => {
    expect(
      videoTaskCardPresentation({
        state: 'succeeded',
        url: 'https://cdn.example/fresh.mp4',
        urlExpired: false,
        playbackStorage: 'upstream-cors-ok',
      })
    ).toBe('inline-play')
  })

  it('is inline-play for tab-local blob urls regardless of storage kind', () => {
    expect(
      videoTaskCardPresentation({
        state: 'succeeded',
        url: 'blob:cached-video',
        playbackStorage: 'upstream-cors-blocked',
      })
    ).toBe('inline-play')
  })
})

describe('videoTaskPlaybackAvailable', () => {
  it('is false for succeeded tasks whose upstream link was stripped on reload', () => {
    expect(
      videoTaskPlaybackAvailable({
        state: 'succeeded',
        url: '',
        urlExpired: true,
      })
    ).toBe(false)
  })

  it('is true only when inline-play presentation applies', () => {
    expect(
      videoTaskPlaybackAvailable({
        state: 'succeeded',
        url: 'https://cdn.example/fresh.mp4',
        urlExpired: false,
        playbackStorage: 'upstream-cors-ok',
      })
    ).toBe(true)
    expect(
      videoTaskPlaybackAvailable({
        state: 'succeeded',
        url: 'https://cdn.example/seedance.mp4',
        urlExpired: false,
        playbackStorage: 'upstream-cors-blocked',
      })
    ).toBe(false)
  })
})

describe('videoCopyLinkAvailable', () => {
  it('is false for inline data:video clips', () => {
    expect(videoCopyLinkAvailable('data:video/mp4;base64,QUJD', 'inline-play')).toBe(false)
  })

  it('is true for shareable http upstream urls when playable', () => {
    expect(videoCopyLinkAvailable('https://cdn.example/v.mp4', 'inline-play')).toBe(true)
  })

  it('is false for download-only presentation', () => {
    expect(videoCopyLinkAvailable('https://cdn.example/v.mp4', 'download-only')).toBe(false)
  })
})

describe('videoTaskCopyLinkAvailable', () => {
  it('matches presentation + inline rules for a task row', () => {
    expect(
      videoTaskCopyLinkAvailable({
        state: 'succeeded',
        url: 'data:video/mp4;base64,QUJD',
        urlExpired: false,
      })
    ).toBe(false)
    expect(
      videoTaskCopyLinkAvailable({
        state: 'succeeded',
        url: 'https://cdn.example/v.mp4',
        urlExpired: false,
        playbackStorage: 'upstream-cors-ok',
      })
    ).toBe(true)
  })
})

describe('groupImageHistoryByTs', () => {
  it('groups multi-model batches and skips single-image rows', () => {
    const img = (ts: number, model: string): ImageHistoryItem => ({
      id: `${ts}-${model}`,
      src: 'https://cdn.example/x.png',
      prompt: 'p',
      model,
      vendorLabel: 'V',
      size: '1:1',
      cost: 0.1,
      ts,
    })
    const runs = groupImageHistoryByTs([img(1, 'a'), img(1, 'b'), img(2, 'solo')])
    expect(runs).toHaveLength(1)
    expect(runs[0].items).toHaveLength(2)
  })
})

describe('groupVideoHistoryByBatch', () => {
  it('groups tasks with the same submittedAtMs', () => {
    const task = (ms: number, model: string): VideoTaskItem => ({
      id: `${ms}-${model}`,
      prompt: 'clip',
      model,
      vendorLabel: 'V',
      seconds: 8,
      estCost: 1,
      keyId: 1,
      state: 'succeeded',
      url: '',
      submittedAtMs: ms,
      elapsedS: 0,
    })
    const runs = groupVideoHistoryByBatch([task(100, 'veo-a'), task(100, 'veo-b'), task(200, 'solo')])
    expect(runs).toHaveLength(1)
    expect(runs[0].items).toHaveLength(2)
  })
})

describe('imageHistoryItemAvailable', () => {
  it('is false when src is empty after reload', () => {
    expect(imageHistoryItemAvailable({ src: '' })).toBe(false)
    expect(imageHistoryItemAvailable({ src: '   ' })).toBe(false)
  })

  it('is true when src is present', () => {
    expect(imageHistoryItemAvailable({ src: 'https://cdn.example/x.png' })).toBe(true)
  })
})

describe('shouldShowStudioSaveReminder', () => {
  it('shows when library or active panels have content', () => {
    expect(shouldShowStudioSaveReminder({ imageCount: 0, videoTaskCount: 0, activeResultCount: 0 })).toBe(false)
    expect(shouldShowStudioSaveReminder({ imageCount: 1, videoTaskCount: 0 })).toBe(true)
    expect(shouldShowStudioSaveReminder({ imageCount: 0, videoTaskCount: 0, activeResultCount: 2 })).toBe(true)
  })
})

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

  it('converts Veo data URIs that carry codec parameters before base64', () => {
    const create = vi.fn(() => 'blob:veo-codecs')
    urlStatic.createObjectURL = create
    urlStatic.revokeObjectURL = vi.fn()
    const { url } = videoPlaybackUrl('data:video/mp4; codecs=avc1;base64,AAAA')
    expect(url).toBe('blob:veo-codecs')
    expect(create).toHaveBeenCalledOnce()
  })

  it('falls back to the original src for a non-base64 data: URI', () => {
    const { url } = videoPlaybackUrl('data:video/mp4,notbase64')
    expect(url).toBe('data:video/mp4,notbase64')
  })

  it('videoPlaybackUrlAsync uses fetch for inline data:video', async () => {
    const create = vi.fn(() => 'blob:async-veo')
    urlStatic.createObjectURL = create
    urlStatic.revokeObjectURL = vi.fn()
    const fetchMock = vi.fn(async () => ({
      blob: async () => new Blob([new Uint8Array([1, 2, 3])], { type: 'video/mp4' }),
    }))
    vi.stubGlobal('fetch', fetchMock)
    const { url, revoke } = await videoPlaybackUrlAsync('data:video/mp4;base64,AAAA')
    expect(fetchMock).toHaveBeenCalled()
    expect(url).toBe('blob:async-veo')
    revoke()
  })
})
