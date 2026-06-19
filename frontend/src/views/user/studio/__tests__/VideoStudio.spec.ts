import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import VideoStudio from '../VideoStudio.vue'

// The component imports the gateway media client; never hit the network in a unit
// test. gatewayVideoFetch is only reached via the lazy on-open URL refresh.
vi.mock('@/api/playground', () => ({
  gatewayVideoSubmit: vi.fn(),
  gatewayVideoFetch: vi.fn(async () => ({ done: true, video_url: 'https://s3.example/fresh.mp4' })),
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

function seedSucceeded(url: string): void {
  const task = {
    id: 'vt_abc',
    prompt: 'a calm mountain lake at dawn',
    model: 'veo-3.1-generate-001',
    vendorLabel: 'Google Vertex',
    seconds: 8,
    aspectRatio: '16:9',
    estCost: 4.8,
    keyId: 1,
    state: 'succeeded',
    url,
    submittedAtMs: Date.now(),
    elapsedS: 8,
  }
  window.localStorage.setItem(`tk_media_lib_v1:${USER_ID}`, JSON.stringify({ images: [], videoTasks: [task] }))
}

const mountStudio = () =>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  mount(VideoStudio as any, {
    props: baseProps,
    global: { plugins: [i18n], stubs: { 'router-link': true, teleport: true } },
  })

describe('VideoStudio succeeded-task presentation', () => {
  beforeEach(() => window.localStorage.clear())

  it('renders a poster tile, NOT an always-on inline <video>, for a succeeded task', () => {
    // Bug 2: the old always-on <video> showed a black, poster-less 0:00 box.
    seedSucceeded('https://s3.example/clip.mp4')
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-play"]').exists()).toBe(true)
    expect(w.find('video').exists()).toBe(false)
    // The per-card status badge is the in-page completion signal that replaced the
    // stale global toast (Bug 1). The runtime-only i18n build returns the key path
    // rather than the translation, so assert on the rendered key.
    expect(w.text()).toContain('statusSucceeded')
  })

  it('plays in-page in the lightbox when the poster is clicked (data: clip, no network)', async () => {
    seedSucceeded('data:video/mp4;base64,AAAA')
    const w = mountStudio()
    expect(w.find('[data-testid="studio-video-preview"]').exists()).toBe(false)
    await w.find('[data-testid="studio-video-play"]').trigger('click')
    await flushPromises()
    const lightbox = w.find('[data-testid="studio-video-preview"]')
    expect(lightbox.exists()).toBe(true)
    expect(lightbox.find('video').exists()).toBe(true)
    expect(lightbox.find('video').attributes('src')).toBe('data:video/mp4;base64,AAAA')
  })

  it('re-mints a fresh presigned URL on open for an http clip (skips re-stream for data:)', async () => {
    const { gatewayVideoFetch } = await import('@/api/playground')
    seedSucceeded('https://s3.example/stale.mp4')
    const w = mountStudio()
    await w.find('[data-testid="studio-video-play"]').trigger('click')
    await flushPromises()
    // openPreview must refresh a short-lived presigned link before playback.
    expect(gatewayVideoFetch).toHaveBeenCalledTimes(1)
    expect(w.find('[data-testid="studio-video-preview"] video').attributes('src')).toBe(
      'https://s3.example/fresh.mp4'
    )
  })

  it('exposes a copy-link affordance in the lightbox ready state (restores the link #860 removed)', async () => {
    // The "看不到 S3 链接" regression: #860 dropped the open-in-new-tab anchor, so an
    // expired card dead-ended. Once the URL is re-minted (ready), the user must be
    // able to grab the link itself — not only Download.
    seedSucceeded('https://s3.example/stale.mp4')
    const w = mountStudio()
    await w.find('[data-testid="studio-video-play"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="studio-video-copy-link"]').exists()).toBe(true)
  })
})
