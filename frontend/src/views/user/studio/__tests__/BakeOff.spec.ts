import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import BakeOff from '../BakeOff.vue'
import * as playground from '@/api/playground'

vi.mock('@/api/playground', () => ({
  gatewayImageGenerations: vi.fn(),
  gatewayGeminiImageViaChat: vi.fn(),
  gatewayVideoSubmit: vi.fn(),
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
  availableIds: new Set([
    'imagen-4.0-fast-generate-001',
    'imagen-4.0-generate-001',
    'gemini-3.1-flash-image',
  ]),
  priceMap: new Map([
    ['imagen-4.0-fast-generate-001', { perImage: 0.02 }],
    ['imagen-4.0-generate-001', { perImage: 0.04 }],
    ['gemini-3.1-flash-image', { perImage: 0.0672 }],
  ]),
  balance: 100,
  keyId: 1,
  keys: [{ id: 1, key: 'sk-test' }],
  rateMultiplier: 1,
}

describe('BakeOff image routing', () => {
  beforeEach(() => {
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
    await tiers[0].trigger('click')
    await tiers[1].trigger('click')
    await tiers[2].trigger('click')
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
      expect.objectContaining({ model: 'imagen-4.0-generate-001', size: '1:1' })
    )
  })
})
