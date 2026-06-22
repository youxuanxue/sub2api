import { nextTick } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'
import { useMediaLibrary, type VideoTaskItem } from '../useMediaLibrary'

const USER_ID = 'media-lib-test'
const KEY = `tk_media_lib_v1:${USER_ID}`

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

  it('preserves http upstream video URLs in localStorage', async () => {
    const lib = useMediaLibrary(USER_ID)
    lib.upsertVideoTask(videoTask('https://cdn.example/video.mp4'))

    await nextTick()
    const persisted = JSON.parse(window.localStorage.getItem(KEY) || '{}')
    expect(persisted.videoTasks[0].url).toBe('https://cdn.example/video.mp4')
  })
})
