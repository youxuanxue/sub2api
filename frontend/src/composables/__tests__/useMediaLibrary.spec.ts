import { nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useMediaLibrary, type ImageHistoryItem, type VideoTaskItem } from '../useMediaLibrary'

vi.mock('@/utils/studioBlobCache.tk', () => ({
  cacheStudioBlobFromSrc: vi.fn(async () => true),
  cacheStudioBlobFromHttpUrl: vi.fn(async () => true),
  getStudioBlobObjectUrl: vi.fn(async (_u: string | number, kind: string, id: string) => {
    if (kind === 'image' && id === 'img-reload') return 'blob:cached-image'
    if (kind === 'video' && id === 'vt-reload') return 'blob:cached-video'
    return ''
  }),
  deleteStudioBlob: vi.fn(async () => undefined),
  pruneStudioBlobCache: vi.fn(async () => undefined),
}))

const USER_ID = 'media-lib-test'
const KEY = `tk_media_lib_v1:${USER_ID}`

function imageItem(id: string, src: string, s3Key?: string): ImageHistoryItem {
  return { id, src, s3Key, prompt: 'a cat', model: 'imagen-4', vendorLabel: 'Vertex', size: '1024x1024', cost: 0.04, ts: 1 }
}

function videoTask(url: string): VideoTaskItem {
  return {
    id: 'vt_inline',
    prompt: 'clip',
    model: 'veo-3.1',
    vendorLabel: 'Vertex',
    seconds: 8,
    estCost: 1,
    keyId: 1,
    state: 'succeeded',
    url,
    submittedAtMs: 1,
    elapsedS: 8,
  }
}

describe('useMediaLibrary video persistence', () => {
  beforeEach(() => window.localStorage.clear())

  it('keeps inline data:video in memory but strips it from localStorage', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.upsertVideoTask(videoTask('data:video/mp4;base64,AAAA'))
    expect(lib.videoTasks.value[0].url).toBe('data:video/mp4;base64,AAAA')

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    expect(persisted.videoTasks[0].url).toBe('')
  })

  it('strips http upstream video URLs from localStorage and marks urlExpired', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.upsertVideoTask(videoTask('https://cdn.example/video.mp4'))
    expect(lib.videoTasks.value[0].url).toBe('https://cdn.example/video.mp4')

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    expect(persisted.videoTasks[0].url).toBe('')
    expect(persisted.videoTasks[0].urlExpired).toBe(true)
  })

  it('reloads legacy persisted http video tasks as prompt-only (urlExpired)', () => {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({
        images: [],
        videoTasks: [
          {
            ...videoTask('https://cdn.example/stale.mp4'),
            prompt: 'neon alley rain',
          },
        ],
      })
    )
    const lib = useMediaLibrary(USER_ID)
    expect(lib.videoTasks.value[0].url).toBe('')
    expect(lib.videoTasks.value[0].urlExpired).toBe(true)
    expect(lib.videoTasks.value[0].prompt).toBe('neon alley rain')
  })

  it('hydrates blank video url from IndexedDB after reload', async () => {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({
        images: [],
        videoTasks: [
          {
            ...videoTask(''),
            id: 'vt-reload',
            url: '',
            urlExpired: true,
            blobCached: true,
          },
        ],
      })
    )
    const lib = useMediaLibrary(USER_ID)
    expect(lib.videoTasks.value[0].url).toBe('')
    await lib.hydrateFromBlobCache()
    expect(lib.videoTasks.value[0].url).toBe('blob:cached-video')
    expect(lib.videoTasks.value[0].urlExpired).toBe(false)
  })

  it('rehydrateVideoFromBlob restores playback url from IndexedDB', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.upsertVideoTask({ ...videoTask(''), id: 'vt-reload', url: '', urlExpired: true })
    const ok = await lib.rehydrateVideoFromBlob('vt-reload')
    expect(ok).toBe(true)
    expect(lib.videoTasks.value[0].url).toBe('blob:cached-video')
    expect(lib.videoTasks.value[0].urlExpired).toBe(false)
  })
})

describe('useMediaLibrary image persistence', () => {
  beforeEach(() => window.localStorage.clear())

  it('keeps inline data:image in memory but strips it from localStorage', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.addImages([imageItem('img-inline', 'data:image/png;base64,AAAA')])
    // In-memory copy keeps the full data: src so the current session still renders it.
    expect(lib.images.value[0].src).toBe('data:image/png;base64,AAAA')

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    expect(persisted.images[0].src).toBe('')
    // Metadata is retained so a reloaded session can still show the regenerate hint.
    expect(persisted.images[0].prompt).toBe('a cat')
  })

  it('strips http and s3Key presigned src from localStorage (reload via IDB/presign)', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.addImages([
      imageItem('img-http', 'https://cdn.example/a.png'),
      imageItem('img-offloaded', 'https://s3.example/presigned', 'media/images/abc.png'),
    ])

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    const byId = Object.fromEntries(persisted.images.map((i: ImageHistoryItem) => [i.id, i]))
    expect(byId['img-http'].src).toBe('')
    expect(byId['img-offloaded'].src).toBe('')
    expect(byId['img-offloaded'].s3Key).toBe('media/images/abc.png')
  })

  it('does not persist blob: src after IndexedDB hydration (second reload safe)', async () => {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({
        images: [{ ...imageItem('img-reload', 'blob:http://localhost/dead'), blobCached: true }],
        videoTasks: [],
      })
    )
    const lib = useMediaLibrary(USER_ID)
    expect(lib.images.value[0].src).toBe('')
    await lib.hydrateFromBlobCache()
    expect(lib.images.value[0].src).toBe('blob:cached-image')

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    expect(persisted.images[0].src).toBe('')
  })

  it('hydrates legacy persisted http image src from the blob cache after reload', async () => {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({
        images: [{ ...imageItem('img-reload', 'https://cdn.example/stale.png'), prompt: 'reload me' }],
        videoTasks: [],
      })
    )
    const lib = useMediaLibrary(USER_ID)
    expect(lib.images.value[0].src).toBe('')
    await lib.hydrateFromBlobCache()
    expect(lib.images.value[0].src).toBe('blob:cached-image')
  })

  it('hydrates blank inline image src from the blob cache after reload', async () => {
    window.localStorage.setItem(
      KEY,
      JSON.stringify({
        images: [{ ...imageItem('img-reload', ''), prompt: 'reload me' }],
        videoTasks: [],
      })
    )
    const lib = useMediaLibrary(USER_ID)
    expect(lib.images.value[0].src).toBe('')
    await lib.hydrateFromBlobCache()
    expect(lib.images.value[0].src).toBe('blob:cached-image')
  })
})
