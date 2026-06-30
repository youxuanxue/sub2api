import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import BakeOff from '../BakeOff.vue'
import * as playground from '@/api/playground'
import type { ImageHistoryItem, VideoTaskItem } from '@/composables/useMediaLibrary'

const libraryMock = vi.hoisted(() => ({
  images: { value: [] as ImageHistoryItem[] },
  videoTasks: { value: [] as VideoTaskItem[] },
  addImages: vi.fn(),
  clearImages: vi.fn(),
  upsertVideoTask: vi.fn(),
  patchVideoTask: vi.fn(),
  removeVideoTask: vi.fn(),
  clearVideoTasks: vi.fn(),
  hydrateFromBlobCache: vi.fn(async () => undefined),
  cacheInlineMedia: vi.fn(async () => undefined),
}))

const appStoreMock = vi.hoisted(() => ({
  showWarning: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStoreMock,
}))

vi.mock('@/api/playground', () => ({
  gatewayImageGenerations: vi.fn(),
  gatewayGeminiImageViaChat: vi.fn(),
  gatewayVideoSubmit: vi.fn(),
  gatewayImagePresign: vi.fn(),
}))

vi.mock('@/composables/useMediaLibrary', () => ({
  useMediaLibrary: () => libraryMock,
}))

vi.mock('@/composables/useVideoTaskPoll', () => ({
  useVideoTaskPoll: () => ({ stopAll: vi.fn(), resume: vi.fn() }),
}))

vi.mock('vue-router', () => ({
  RouterLink: { template: '<a><slot /></a>' },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: { en },
})

const baseProps = {
  apiKey: 'sk-test',
  gatewayBase: 'https://api.example.com',
  userId: 42,
  availableIds: new Set([
    'imagen-4.0-fast-generate-001',
    'imagen-4.0-generate-001',
    'imagen-4.0-ultra-generate-001',
    'gemini-3.1-flash-image',
    'seedream-4-0-250828',
  ]),
  priceMap: new Map([
    ['imagen-4.0-fast-generate-001', { perImage: 0.02 }],
    ['imagen-4.0-generate-001', { perImage: 0.04 }],
    ['imagen-4.0-ultra-generate-001', { perImage: 0.06 }],
    ['gemini-3.1-flash-image', { perImage: 0.0672 }],
    ['seedream-4-0-250828', { perImage: 0.0299 }],
  ]),
  balance: 100,
  keyId: 1,
  keys: [{ id: 1, key: 'sk-test' }],
  rateMultiplier: 1,
}

const videoProps = {
  ...baseProps,
  availableIds: new Set([
    'seedance-1-0-pro-250528',
    'doubao-seedance-2-0-fast-260128',
    'veo-3.1-generate-001',
  ]),
  priceMap: new Map([
    ['seedance-1-0-pro-250528', { perSecond: 0.1088 }],
    ['doubao-seedance-2-0-fast-260128', { perSecond: 0.1194 }],
    ['veo-3.1-generate-001', { perSecond: 0.6 }],
  ]),
}

describe('BakeOff run gate', () => {
  beforeEach(() => {
    libraryMock.videoTasks.value = []
    vi.mocked(playground.gatewayVideoSubmit).mockReset()
    vi.mocked(playground.gatewayVideoSubmit).mockResolvedValue({ id: 'vt_busy', status: 'processing' })
  })

  it('blocks a second video run while panels are still processing', async () => {
    const wrapper = mount(BakeOff, {
      props: videoProps,
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('family in a garden')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    expect(playground.gatewayVideoSubmit).toHaveBeenCalledTimes(2)
    const runBtn = wrapper.get('[data-testid="studio-bakeoff-run"]')
    expect(runBtn.attributes('disabled')).toBeDefined()
    expect(runBtn.text()).toContain('studio.bakeoff.running')

    await runBtn.trigger('click')
    await flushPromises()
    expect(playground.gatewayVideoSubmit).toHaveBeenCalledTimes(2)
  })

  it('allows regenerate after every panel reaches a terminal state', async () => {
    vi.mocked(playground.gatewayVideoSubmit)
      .mockResolvedValueOnce({ id: 'vt_a', status: 'succeeded', url: 'https://cdn/a.mp4' })
      .mockResolvedValueOnce({ id: 'vt_b', status: 'succeeded', url: 'https://cdn/b.mp4' })

    const wrapper = mount(BakeOff, {
      props: videoProps,
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('family in a garden')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    expect(playground.gatewayVideoSubmit).toHaveBeenCalledTimes(2)
    const runBtn = wrapper.get('[data-testid="studio-bakeoff-run"]')
    expect(runBtn.attributes('disabled')).toBeUndefined()
    expect(runBtn.text()).toContain('studio.bakeoff.regenerate')

    vi.mocked(playground.gatewayVideoSubmit).mockResolvedValue({ id: 'vt_redo', status: 'processing' })
    await runBtn.trigger('click')
    await flushPromises()
    expect(playground.gatewayVideoSubmit).toHaveBeenCalledTimes(4)
  })
})

describe('BakeOff image routing', () => {
  beforeEach(() => {
    libraryMock.images.value = []
    libraryMock.videoTasks.value = []
    libraryMock.addImages.mockReset()
    libraryMock.addImages.mockImplementation((items: ImageHistoryItem[]) => {
      libraryMock.images.value = [...items, ...libraryMock.images.value]
    })
    vi.mocked(playground.gatewayImageGenerations).mockReset()
    vi.mocked(playground.gatewayGeminiImageViaChat).mockReset()
    vi.mocked(playground.gatewayImageGenerations).mockResolvedValue({
      data: [{ url: 'https://cdn.example/imagen.png' }],
    })
    vi.mocked(playground.gatewayGeminiImageViaChat).mockResolvedValue({
      choices: [{ message: { content: '![image](data:image/png;base64,abc)' } }],
    })
  })

  it('routes gemini-native image models via chat completions', async () => {
    const wrapper = mount(BakeOff, {
      props: baseProps,
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    await wrapper.get('[data-testid="bakeoff-mode-image"]').trigger('click')
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    // sorted cheap→premium: fast, seedream, standard, ultra, gemini
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await tiers[4].trigger('click')
    await wrapper.get('textarea').setValue('a red apple')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    expect(playground.gatewayGeminiImageViaChat).toHaveBeenCalledWith(
      'sk-test',
      'https://api.example.com',
      expect.objectContaining({ model: 'gemini-3.1-flash-image', aspectRatio: '1:1' })
    )
    expect(playground.gatewayImageGenerations).toHaveBeenCalledWith(
      'sk-test',
      'https://api.example.com',
      expect.objectContaining({ model: 'imagen-4.0-fast-generate-001', size: '1:1' })
    )
    expect(playground.gatewayImageGenerations).toHaveBeenCalledWith(
      'sk-test',
      'https://api.example.com',
      expect.objectContaining({ model: 'seedream-4-0-250828', size: '1:1' })
    )
    expect(libraryMock.addImages).toHaveBeenCalled()
  })

  it('allows selecting five image models for bake-off', async () => {
    const wrapper = mount(BakeOff, {
      props: baseProps,
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    await wrapper.get('[data-testid="bakeoff-mode-image"]').trigger('click')
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    expect(tiers.length).toBe(5)
    for (const tier of tiers) {
      await tier.trigger('click')
    }
    expect(tiers.filter((t) => t.classes().some((c) => c.includes('bg-primary-600'))).length).toBe(5)
  })

  it('persists to shared library and keeps history after clearing the current view', async () => {
    const wrapper = mount(BakeOff, {
      props: baseProps,
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    await wrapper.get('[data-testid="bakeoff-mode-image"]').trigger('click')
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('a red apple')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    expect(libraryMock.addImages).toHaveBeenCalledTimes(1)
    expect(libraryMock.addImages.mock.calls[0][0]).toHaveLength(2)
    expect(wrapper.findAll('[data-testid="bakeoff-panel"]').length).toBe(2)
    expect(wrapper.find('[data-testid="studio-bakeoff-save-reminder"]').exists()).toBe(true)

    await wrapper.get('[data-testid="studio-bakeoff-clear-results"]').trigger('click')
    expect(wrapper.findAll('[data-testid="bakeoff-panel"]').length).toBe(0)
    expect(wrapper.find('[data-testid="bakeoff-history"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="bakeoff-history-item"]').length).toBe(2)
  })

  it('opens history video previews at full lightbox size', async () => {
    const batchMs = 1710000000000
    libraryMock.videoTasks.value = [
      {
        id: 'vt_a',
        prompt: 'a red apple',
        model: 'veo-3.1-generate-001',
        vendorLabel: 'Google Vertex',
        seconds: 8,
        estCost: 4.8,
        keyId: 1,
        state: 'succeeded',
        url: 'https://cdn.example/a.mp4',
        submittedAtMs: batchMs,
        elapsedS: 8,
      },
      {
        id: 'vt_b',
        prompt: 'a red apple',
        model: 'seedance-1-0-pro-250528',
        vendorLabel: 'Doubao',
        seconds: 10,
        estCost: 1.09,
        keyId: 1,
        state: 'succeeded',
        url: 'https://cdn.example/b.mp4',
        submittedAtMs: batchMs,
        elapsedS: 10,
      },
    ]
    const wrapper = mount(BakeOff, {
      props: baseProps,
      global: { plugins: [i18n], stubs: { RouterLink: true, teleport: true } },
    })

    await wrapper.get('[data-testid="bakeoff-history-item"] button').trigger('click')
    await flushPromises()

    const previewVideo = wrapper.get('[data-testid="bakeoff-video-preview"] video')
    expect(previewVideo.attributes('src')).toBe('https://cdn.example/a.mp4')
    expect(previewVideo.classes()).toEqual(
      expect.arrayContaining(['h-full', 'w-full', 'object-contain', 'max-h-full', 'max-w-full'])
    )
  })

  it('keeps the lightbox open with copy-link fallback when preview media fails', async () => {
    vi.mocked(playground.gatewayVideoSubmit)
      .mockResolvedValueOnce({ id: 'vt_panel_a', status: 'succeeded', url: 'https://cdn.example/a.mp4' })
      .mockResolvedValueOnce({ id: 'vt_panel_b', status: 'succeeded', url: 'https://cdn.example/b.mp4' })

    const wrapper = mount(BakeOff, {
      props: videoProps,
      global: { plugins: [i18n], stubs: { RouterLink: true, teleport: true } },
    })
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('family in a garden')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    const panelEls = wrapper.findAll('[data-testid="bakeoff-panel"]')
    await panelEls[0].get('[data-testid="bakeoff-video-play"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="bakeoff-video-preview"] video').exists()).toBe(true)

    await wrapper.find('[data-testid="bakeoff-video-preview"] video').trigger('error')
    await flushPromises()

    expect(wrapper.find('[data-testid="bakeoff-video-preview"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="bakeoff-video-copy-link"]').length).toBeGreaterThan(0)
    expect(libraryMock.patchVideoTask).toHaveBeenCalledWith('vt_panel_a', { urlExpired: true })
    expect(panelEls[0].find('[data-testid="bakeoff-video-play"]').exists()).toBe(false)
    expect(panelEls[0].find('[data-testid="bakeoff-video-expired"]').exists()).toBe(true)
    expect(panelEls[0].find('[data-testid="bakeoff-video-download"]').exists()).toBe(true)
    expect(panelEls[0].find('[data-testid="bakeoff-video-copy-card-link"]').exists()).toBe(true)
    expect(panelEls[1].find('[data-testid="bakeoff-video-play"]').exists()).toBe(true)
  })

  it('exposes card-level copy-link for succeeded video panels', async () => {
    vi.mocked(playground.gatewayVideoSubmit)
      .mockResolvedValueOnce({ id: 'vt_panel_a', status: 'succeeded', url: 'https://cdn.example/a.mp4' })
      .mockResolvedValueOnce({ id: 'vt_panel_b', status: 'succeeded', url: 'https://cdn.example/b.mp4' })

    const writeText = vi.fn(async () => undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })

    const wrapper = mount(BakeOff, {
      props: videoProps,
      global: { plugins: [i18n], stubs: { RouterLink: true, teleport: true } },
    })
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('family in a garden')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    await wrapper.findAll('[data-testid="bakeoff-panel"]')[0].get('[data-testid="bakeoff-video-copy-card-link"]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith('https://cdn.example/a.mp4')
    vi.unstubAllGlobals()
  })

  it('warns instead of opening a new tab when downloading an expired panel url', async () => {
    vi.mocked(playground.gatewayVideoSubmit)
      .mockResolvedValueOnce({ id: 'vt_panel_a', status: 'succeeded', url: 'https://cdn.example/a.mp4' })
      .mockResolvedValueOnce({ id: 'vt_panel_b', status: 'succeeded', url: 'https://cdn.example/b.mp4' })
    const downloadSpy = vi.spyOn(await import('@/utils/studioDownload.tk'), 'downloadMedia')

    const wrapper = mount(BakeOff, {
      props: videoProps,
      global: { plugins: [i18n], stubs: { RouterLink: true, teleport: true } },
    })
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('family in a garden')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    const panelEls = wrapper.findAll('[data-testid="bakeoff-panel"]')
    await panelEls[0].get('[data-testid="bakeoff-video-play"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="bakeoff-video-preview"] video').trigger('error')
    await flushPromises()

    appStoreMock.showWarning.mockClear()
    downloadSpy.mockClear()
    await panelEls[0].get('[data-testid="bakeoff-video-download"]').trigger('click')
    expect(appStoreMock.showWarning).toHaveBeenCalled()
    expect(downloadSpy).not.toHaveBeenCalled()
    downloadSpy.mockRestore()
  })

  it('converts inline data:video panel urls to blob playback in the lightbox', async () => {
    const createObjectURL = vi.fn(() => 'blob:preview')
    vi.stubGlobal(
      'URL',
      Object.assign({}, URL, {
        createObjectURL,
        revokeObjectURL: vi.fn(),
      })
    )
    vi.mocked(playground.gatewayVideoSubmit)
      .mockResolvedValueOnce({
        id: 'vt_data',
        done: true,
        response: { videos: [{ bytesBase64Encoded: 'QUFB', mimeType: 'video/mp4' }] },
      })
      .mockResolvedValueOnce({ id: 'vt_http', status: 'succeeded', url: 'https://cdn.example/b.mp4' })

    const wrapper = mount(BakeOff, {
      props: videoProps,
      global: { plugins: [i18n], stubs: { RouterLink: true, teleport: true } },
    })
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await wrapper.get('textarea').setValue('family in a garden')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    const panelEls = wrapper.findAll('[data-testid="bakeoff-panel"]')
    await panelEls[0].get('[data-testid="bakeoff-video-play"]').trigger('click')
    await flushPromises()

    expect(createObjectURL).toHaveBeenCalled()
    expect(wrapper.get('[data-testid="bakeoff-video-preview"] video').attributes('src')).toBe('blob:preview')
    vi.unstubAllGlobals()
  })

  it('shows friendly panel error for codex unsupported gemini image', async () => {
    vi.mocked(playground.gatewayGeminiImageViaChat).mockRejectedValueOnce(
      new Error(
        '{"message":"The \'gemini-3.1-flash-image\' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}'
      )
    )
    const wrapper = mount(BakeOff, {
      props: baseProps,
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    await wrapper.get('[data-testid="bakeoff-mode-image"]').trigger('click')
    const tiers = wrapper.findAll('[data-testid="bakeoff-tier"]')
    await tiers[0].trigger('click')
    await tiers[4].trigger('click')
    await wrapper.get('textarea').setValue('a red apple')
    await wrapper.get('[data-testid="studio-bakeoff-run"]').trigger('click')
    await flushPromises()

    const panelText = wrapper.findAll('[data-testid="bakeoff-panel"]').map((p) => p.text()).join('\n')
    expect(panelText).not.toContain('invalid_request_error')
    expect(panelText).not.toContain('ChatGPT account')
    expect(panelText).toMatch(/cannot serve that model|不支持该模型|studio\.errors\.unsupported_model/)
  })
})
