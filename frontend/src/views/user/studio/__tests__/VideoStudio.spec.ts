import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import type { VideoTaskItem } from '@/composables/useMediaLibrary'
import VideoStudio from '../VideoStudio.vue'

const libraryMock = vi.hoisted(() => ({
  usePersistedLibrary: true,
  videoTasks: { value: [] as Array<Record<string, unknown>> },
  patchVideoTaskSpy: vi.fn(),
}))

vi.mock('@/composables/useMediaLibrary', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useMediaLibrary')>()
  return {
    ...actual,
    useMediaLibrary: (userId: number | string) => {
      if (libraryMock.usePersistedLibrary) {
        return actual.useMediaLibrary(userId)
      }
      return {
        images: ref([]),
        videoTasks: libraryMock.videoTasks,
        addImages: vi.fn(),
        clearImages: vi.fn(),
        upsertVideoTask: vi.fn(),
        patchVideoTask: libraryMock.patchVideoTaskSpy,
        removeVideoTask: vi.fn(),
        clearVideoTasks: vi.fn(),
        hydrateFromBlobCache: vi.fn(async () => undefined),
        cacheInlineMedia: vi.fn(async () => undefined),
      }
    },
  }
})

vi.mock('@/api/playground', () => ({
  gatewayVideoSubmit: vi.fn(),
  gatewayVideoFetch: vi.fn(async () => ({ done: true, video_url: 'https://s3.example/fresh.mp4' })),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showWarning: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

const i18n = createI18n({ legacy: false, locale: 'en', fallbackWarn: false, missingWarn: false, messages: { en } })

const USER_ID = 'u-video-test'

const baseProps = {
  apiKey: 'sk-test',
  gatewayBase: 'https://gw.example',
  availableIds: new Set<string>(),
  priceMap: new Map(),
  balance: 100,
  userId: USER_ID,
  keyId: 1,
  keys: [{ id: 1, key: 'sk-test' }],
  rateMultiplier: 1,
}

function baseTask(overrides: Partial<VideoTaskItem> = {}): VideoTaskItem {
  return {
    id: 'vt_abc',
    prompt: 'a calm mountain lake at dawn',
    model: 'veo-3.1-generate-001',
    vendorLabel: 'Google Vertex',
    seconds: 8,
    aspectRatio: '16:9',
    estCost: 4.8,
    keyId: 1,
    state: 'succeeded',
    url: 'https://cdn.example/upstream.mp4',
    playbackStorage: 'upstream-cors-ok',
    submittedAtMs: Date.now(),
    elapsedS: 8,
    ...overrides,
  }
}

function seedPersisted(tasks: VideoTaskItem[]): void {
  libraryMock.usePersistedLibrary = true
  window.localStorage.setItem(`tk_media_lib_v1:${USER_ID}`, JSON.stringify({ images: [], videoTasks: tasks }))
}

function seedInSession(task: VideoTaskItem): void {
  libraryMock.usePersistedLibrary = false
  libraryMock.videoTasks.value = [task as unknown as Record<string, unknown>]
}

const mountStudio = () =>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  mount(VideoStudio as any, {
    props: baseProps,
    global: { plugins: [i18n], stubs: { 'router-link': true, teleport: true } },
  })

describe('VideoStudio succeeded-task presentation', () => {
  beforeEach(() => {
    window.localStorage.clear()
    libraryMock.usePersistedLibrary = true
    libraryMock.videoTasks.value = []
    libraryMock.patchVideoTaskSpy.mockReset()
    libraryMock.patchVideoTaskSpy.mockImplementation((id: string, patch: Partial<VideoTaskItem>) => {
      libraryMock.videoTasks.value = libraryMock.videoTasks.value.map((task) =>
        task.id === id ? { ...task, ...patch } : task
      )
    })
    Object.defineProperty(URL, 'createObjectURL', {
      value: vi.fn(() => 'blob:https://studio.test/video-preview'),
      configurable: true,
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      value: vi.fn(),
      configurable: true,
    })
  })

  it('renders a poster tile, NOT an always-on inline <video>, for a fresh in-session task', () => {
    seedInSession(baseTask({ url: 'https://s3.example/clip.mp4', playbackStorage: 'upstream-cors-ok' }))
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(true)
    expect(w.find('video').exists()).toBe(false)
    expect(w.text()).toContain('statusSucceeded')
  })

  it('does not resurrect a persisted inline data: clip after reload when blob cache is empty', () => {
    seedPersisted([baseTask({ url: 'data:video/mp4;base64,AAAA' })])
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(false)
    expect(w.find('[data-testid="studio-video-expired"]').exists()).toBe(true)
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('shows prompt-only card for reloaded http upstream clips (no play tile)', () => {
    seedPersisted([
      baseTask({
        id: 'vt_stale',
        prompt: 'neon Tokyo alley in rain',
        url: 'https://cdn.example/stale.mp4',
      }),
    ])
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(false)
    expect(w.find('[data-testid="studio-video-expired"]').exists()).toBe(true)
    expect(w.text()).toContain('neon Tokyo alley in rain')
    expect(w.text()).toContain('studio.playback.expired')
  })

  it('keeps the card play tile when preview media fails in the lightbox', async () => {
    seedInSession(baseTask())
    const w = mountStudio()
    await w.find('[data-testid="studio-video-play"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="studio-video-preview"] video').exists()).toBe(true)

    await w.find('[data-testid="studio-video-preview"] video').trigger('error')
    await flushPromises()

    expect(w.find('[data-testid="studio-video-preview"]').exists()).toBe(true)
    expect(w.findAll('[data-testid="studio-video-copy-link"]').length).toBeGreaterThan(0)
    expect(libraryMock.patchVideoTaskSpy).not.toHaveBeenCalledWith('vt_abc', { urlExpired: true })
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(true)
    expect(w.find('[data-testid="studio-video-expired"]').exists()).toBe(false)
    expect(w.find('[data-testid="studio-video-download"]').exists()).toBe(true)
    expect(w.find('[data-testid="studio-video-copy-card-link"]').exists()).toBe(true)
    expect(libraryMock.videoTasks.value[0].url).toBe('https://cdn.example/upstream.mp4')
  })

  it('plays an http upstream clip directly without re-fetching through TokenKey', async () => {
    const { gatewayVideoFetch } = await import('@/api/playground')
    seedInSession(baseTask())
    const w = mountStudio()
    await w.find('[data-testid="studio-video-play"]').trigger('click')
    await flushPromises()
    expect(gatewayVideoFetch).not.toHaveBeenCalled()
    const previewVideo = w.find('[data-testid="studio-video-preview"] video')
    expect(previewVideo.attributes('src')).toBe('https://cdn.example/upstream.mp4')
    expect(previewVideo.classes()).toEqual(
      expect.arrayContaining(['h-full', 'w-full', 'object-contain', 'max-h-full', 'max-w-full'])
    )
  })

  it('shows download-first card for upstream CORS-blocked clips (no play tile)', () => {
    seedInSession(
      baseTask({
        url: 'https://cdn.volcengine.example/seedance.mp4',
        playbackStorage: 'upstream-cors-blocked',
      })
    )
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(false)
    expect(w.find('[data-testid="studio-video-download-only"]').exists()).toBe(true)
    expect(w.find('[data-testid="studio-video-download-primary"]').exists()).toBe(true)
    expect(w.find('[data-testid="studio-video-download"]').exists()).toBe(false)
  })

  it('shows checking state before upstream CORS classification completes', () => {
    seedInSession(baseTask({ url: 'https://cdn.example/pending.mp4', playbackStorage: undefined }))
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-checking"]').exists()).toBe(true)
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(false)
  })

  it('exposes a copy-link affordance in the lightbox ready state', async () => {
    seedInSession(baseTask())
    const w = mountStudio()
    await w.find('[data-testid="studio-video-play"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="studio-video-copy-link"]').exists()).toBe(true)
  })
})

describe('VideoStudio model catalog loading', () => {
  beforeEach(() => {
    window.localStorage.clear()
    libraryMock.usePersistedLibrary = true
    libraryMock.videoTasks.value = []
  })

  it('shows loading instead of the empty-state footgun while catalogLoading', () => {
    const w = mount(VideoStudio as never, {
      props: { ...baseProps, catalogLoading: true },
      global: { plugins: [i18n], stubs: { 'router-link': true, teleport: true } },
    })
    expect(w.find('[data-testid="studio-video-model-loading"]').exists()).toBe(true)
    expect(w.find('[data-testid="studio-video-model-empty"]').exists()).toBe(false)
  })
})
