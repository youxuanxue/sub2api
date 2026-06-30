import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import StudioVideoPreviewLightbox from '../StudioVideoPreviewLightbox.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: { en },
})

function mountLightbox(overrides: Record<string, unknown> = {}) {
  return mount(StudioVideoPreviewLightbox, {
    props: {
      open: true,
      previewState: 'ready',
      previewUrl: 'https://cdn.example/clip.mp4',
      downloadUrl: 'https://cdn.example/clip.mp4',
      downloadFilename: 'tokenkey-test.mp4',
      label: 'Seedance 1.0',
      cost: 1.09,
      previewMediaReady: true,
      copiedLink: false,
      testId: 'studio-video-preview',
      ...overrides,
    },
    global: { plugins: [i18n], stubs: { teleport: true } },
  })
}

describe('StudioVideoPreviewLightbox', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'URL',
      Object.assign({}, URL, {
        createObjectURL: vi.fn(() => 'blob:preview'),
        revokeObjectURL: vi.fn(),
      })
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders a full-size video element for ready state', () => {
    const wrapper = mountLightbox()
    const video = wrapper.get('[data-testid="studio-video-preview"] video')
    expect(video.attributes('src')).toBe('https://cdn.example/clip.mp4')
    expect(video.classes()).toEqual(
      expect.arrayContaining(['h-full', 'w-full', 'object-contain', 'max-h-full', 'max-w-full'])
    )
  })

  it('emits error when the video element fails to load', async () => {
    const wrapper = mountLightbox()
    await wrapper.get('[data-testid="studio-video-preview"] video').trigger('error')
    expect(wrapper.emitted('error')).toHaveLength(1)
  })

  it('shows retry affordance in expired state', () => {
    const wrapper = mountLightbox({ previewState: 'expired', previewUrl: '' })
    expect(wrapper.text()).toContain('studio.video.retry')
  })
})
