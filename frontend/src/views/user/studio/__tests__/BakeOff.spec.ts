import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import BakeOff from '../BakeOff.vue'
import * as playground from '@/api/playground'
import type { ImageHistoryItem } from '@/composables/useMediaLibrary'

const libraryMock = vi.hoisted(() => ({
  images: { value: [] as ImageHistoryItem[] },
  videoTasks: { value: [] },
  addImages: vi.fn(),
  clearImages: vi.fn(),
  upsertVideoTask: vi.fn(),
  patchVideoTask: vi.fn(),
  removeVideoTask: vi.fn(),
  clearVideoTasks: vi.fn(),
  hydrateFromBlobCache: vi.fn(async () => undefined),
  cacheInlineMedia: vi.fn(async () => undefined),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showWarning: vi.fn() }),
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
