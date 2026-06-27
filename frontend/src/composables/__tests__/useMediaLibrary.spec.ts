import { nextTick } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'
import { useMediaLibrary, type ImageHistoryItem, type VideoTaskItem } from '../useMediaLibrary'

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

  it('preserves offloaded (s3Key) and http image URLs in localStorage', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.addImages([
      imageItem('img-http', 'https://cdn.example/a.png'),
      imageItem('img-offloaded', 'https://s3.example/presigned', 'media/images/abc.png'),
    ])

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    const byId = Object.fromEntries(persisted.images.map((i: ImageHistoryItem) => [i.id, i.src]))
    expect(byId['img-http']).toBe('https://cdn.example/a.png')
    expect(byId['img-offloaded']).toBe('https://s3.example/presigned')
  })
})
