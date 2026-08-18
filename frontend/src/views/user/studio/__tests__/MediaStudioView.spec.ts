import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import MediaStudioView from '../MediaStudioView.vue'

const { listKeys, gatewayListModels, getMePricingCatalog, getPublicPricing, getAPIKeyCapabilities } =
  vi.hoisted(() => ({
    listKeys: vi.fn(),
    gatewayListModels: vi.fn(),
    getMePricingCatalog: vi.fn(),
    getPublicPricing: vi.fn(),
    getAPIKeyCapabilities: vi.fn(),
  }))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: listKeys },
}))

vi.mock('@/api/playground', () => ({
  gatewayListModels,
  resolveGatewayBaseUrl: vi.fn((base?: string) => base || 'https://gw.example'),
}))

vi.mock('@/api/me-pricing', () => ({
  getMePricingCatalog,
}))

vi.mock('@/api/pricing', () => ({
  getPublicPricing,
}))

vi.mock('@/api/api-key-capabilities', () => ({
  getAPIKeyCapabilities,
}))

const fetchPublicSettings = vi.fn(async () => undefined)
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    fetchPublicSettings,
    apiBaseUrl: 'https://api.example',
    cachedPublicSettings: { api_base_url: 'https://api.example' },
  }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 1, balance: 100 },
    refreshUser: vi.fn(),
  }),
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div><slot /></div>' },
}))

vi.mock('../ChatStudio.vue', () => ({
  default: {
    name: 'ChatStudio',
    props: ['apiKey', 'gatewayBase', 'availableIds'],
    template: '<div data-testid="studio-chat-panel">chat</div>',
  },
}))

vi.mock('../ImageStudio.vue', () => ({ default: { template: '<div />' } }))
vi.mock('../VideoStudio.vue', () => ({
  default: {
    name: 'VideoStudio',
    props: ['apiKey', 'catalogLoading', 'priceMap', 'availableIds'],
    template: `<div>
      <div data-testid="studio-video-key">{{ apiKey }}</div>
      <div v-if="catalogLoading" data-testid="studio-video-catalog-loading">loading</div>
      <div v-else data-testid="studio-video-catalog-ready">ready</div>
    </div>`,
  },
}))
vi.mock('../BakeOff.vue', () => ({
  default: {
    name: 'BakeOff',
    props: ['apiKey', 'catalogLoading', 'priceMap', 'availableIds', 'keyId'],
    emits: ['modality-change', 'spent'],
    template: `<div data-testid="bakeoff-stub">
      <span data-testid="bakeoff-stub-key">{{ apiKey }}</span>
      <button data-testid="bakeoff-stub-image" @click="$emit('modality-change', 'image')">image</button>
      <button data-testid="bakeoff-stub-video" @click="$emit('modality-change', 'video')">video</button>
    </div>`,
  },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: { en },
})

describe('MediaStudioView bootstrap', () => {
  beforeEach(() => {
    listKeys.mockReset()
    gatewayListModels.mockReset()
    getMePricingCatalog.mockReset()
    getPublicPricing.mockReset()
    getAPIKeyCapabilities.mockReset()
    fetchPublicSettings.mockClear()

    listKeys.mockResolvedValue({
      items: [
        { id: 1, name: 'trial', key: 'sk-test', status: 'active', group: { id: 10, name: 'default' } },
      ],
    })
    gatewayListModels.mockResolvedValue({ data: [{ id: 'gpt-4o' }] })
    getPublicPricing.mockResolvedValue({ data: [] })
    getAPIKeyCapabilities.mockResolvedValue({ api_key_id: 1, routing_mode: 'direct', models: [] })
  })

  it('mounts ChatStudio after probing only the seed group, not every distinct group', async () => {
    listKeys.mockResolvedValue({
      items: [
        { id: 1, name: 'trial', key: 'sk-a', status: 'active', group: { id: 10, name: 'g1' } },
        { id: 2, name: 'other', key: 'sk-b', status: 'active', group: { id: 20, name: 'g2' } },
      ],
    })
    let resolveSecond!: (value: { data: { id: string }[] }) => void
    gatewayListModels.mockImplementation((key: string) => {
      if (key === 'sk-a') return Promise.resolve({ data: [{ id: 'gpt-4o' }] })
      return new Promise((resolve) => {
        resolveSecond = resolve
      })
    })
    getMePricingCatalog.mockResolvedValue({ authorized_groups_by_model: {}, models: [] })

    const wrapper = mount(MediaStudioView, {
      global: {
        plugins: [i18n],
        stubs: { 'router-link': true },
      },
    })

    await flushPromises()
    expect(wrapper.find('[data-testid="studio-chat-panel"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="studio-bootstrap-loading"]').exists()).toBe(false)
    expect(gatewayListModels).toHaveBeenCalledWith('sk-a', 'https://api.example')

    resolveSecond({ data: [{ id: 'claude-3-opus' }] })
    await flushPromises()
    expect(gatewayListModels).toHaveBeenCalledWith('sk-b', 'https://api.example')
  })

  it('mounts ChatStudio after model probe without waiting for the per-key price catalog', async () => {
    let resolveCatalog!: (value: { models: [] }) => void
    getMePricingCatalog.mockImplementation((opts?: { apiKeyId?: number }) => {
      if (opts?.apiKeyId != null) {
        return new Promise<{ models: [] }>((resolve) => {
          resolveCatalog = resolve
        })
      }
      return Promise.resolve({ authorized_groups_by_model: {}, models: [] })
    })

    const wrapper = mount(MediaStudioView, {
      global: {
        plugins: [i18n],
        stubs: { 'router-link': true },
      },
    })

    await flushPromises()
    expect(wrapper.find('[data-testid="studio-chat-panel"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="studio-bootstrap-loading"]').exists()).toBe(false)

    resolveCatalog({ models: [] })
    await flushPromises()
  })

  it('loads the selected automatic-routing key from the capability SSOT', async () => {
    listKeys.mockResolvedValue({
      items: [
        {
          id: 9,
          name: 'universal',
          key: 'sk-uni',
          status: 'active',
          routing_mode: 'universal',
          group_id: null,
        },
      ],
    })
    getAPIKeyCapabilities.mockResolvedValue({
      api_key_id: 9,
      routing_mode: 'universal',
      models: [
        {
          id: 'doubao-seedance-1-0-pro-250528',
          protocols: ['openai'],
          modalities: ['video'],
          routes: [],
        },
      ],
    })
    getPublicPricing.mockResolvedValue({
      data: [{ model_id: 'doubao-seedance-1-0-pro-250528', pricing: { output_cost_per_second: 0.1088 } }],
    })
    gatewayListModels.mockResolvedValue({ data: [] })

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })
    await flushPromises()
    await flushPromises()

    expect(getAPIKeyCapabilities).toHaveBeenCalledWith(9)
    expect(getPublicPricing).toHaveBeenCalled()
    expect(wrapper.text()).toContain('studio.universalKeyBadge')
    expect(getMePricingCatalog).not.toHaveBeenCalled()
    expect(gatewayListModels).not.toHaveBeenCalledWith('sk-uni', expect.anything())
  })

  it('shows a load failure when automatic-routing capabilities cannot be read', async () => {
    listKeys.mockResolvedValue({
      items: [
        {
          id: 9,
          name: 'automatic',
          key: 'sk-auto',
          status: 'active',
          routing_mode: 'universal',
          group_id: null,
        },
      ],
    })
    getAPIKeyCapabilities.mockRejectedValue(new Error('capability unavailable'))

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('capability unavailable')
    expect(wrapper.find('[data-testid="studio-chat-panel"]').exists()).toBe(false)
  })

  it('uses capability modalities instead of public pricing to select and scope automatic keys', async () => {
    listKeys.mockResolvedValue({
      items: [
        {
          id: 9,
          name: 'trial',
          key: 'sk-chat',
          status: 'active',
          routing_mode: 'universal',
          group_id: null,
        },
        {
          id: 10,
          name: 'video',
          key: 'sk-video',
          status: 'active',
          routing_mode: 'universal',
          group_id: null,
        },
      ],
    })
    getAPIKeyCapabilities.mockImplementation(async (keyId: number) => ({
      api_key_id: keyId,
      routing_mode: 'universal',
      models: keyId === 9
        ? [{ id: 'pricing-says-video', protocols: ['openai'], modalities: ['chat'], routes: [] }]
        : [{ id: 'capability-says-video', protocols: ['openai'], modalities: ['video'], routes: [] }],
    }))
    getPublicPricing.mockResolvedValue({
      data: [
        { model_id: 'pricing-says-video', billing_mode: 'video', pricing: { output_cost_per_second: 0.1 } },
        { model_id: 'capability-says-video', billing_mode: 'image', pricing: { output_cost_per_image: 0.1 } },
      ],
    })

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })
    await flushPromises()
    await flushPromises()

    await wrapper.find('[data-testid="studio-mode-video"]').trigger('click')
    await flushPromises()

    const videoStudio = wrapper.findComponent({ name: 'VideoStudio' })
    expect(videoStudio.props('apiKey')).toBe('sk-video')
    expect([...videoStudio.props('availableIds') as Set<string>]).toEqual(['capability-says-video'])
  })

  it('shows video catalog loading until per-key prices resolve after switching from chat', async () => {
    listKeys.mockResolvedValue({
      items: [{ id: 1, name: 'trial', key: 'sk-test', status: 'active', group: { id: 10, name: 'g1' } }],
    })
    gatewayListModels.mockResolvedValue({ data: [{ id: 'doubao-seedance-1-0-pro-250528' }] })
    getPublicPricing.mockResolvedValue({
      data: [
        {
          model_id: 'doubao-seedance-1-0-pro-250528',
          billing_mode: 'video',
          pricing: { output_cost_per_second: 0.1088 },
        },
      ],
    })
    let resolvePerKey!: (value: { models: { model_id: string; billing_mode: string; your_price: { currency: string; per_second: number } }[] }) => void
    const perKeyPending = new Promise<{ models: { model_id: string; billing_mode: string; your_price: { currency: string; per_second: number } }[] }>(
      (resolve) => {
        resolvePerKey = resolve
      }
    )
    getMePricingCatalog.mockImplementation((opts?: { apiKeyId?: number }) => {
      if (opts?.apiKeyId != null) return perKeyPending
      return Promise.resolve({ authorized_groups_by_model: {}, models: [] })
    })

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })

    await flushPromises()
    expect(wrapper.find('[data-testid="studio-chat-panel"]').exists()).toBe(true)

    await wrapper.find('[data-testid="studio-mode-video"]').trigger('click')
    await wrapper.vm.$nextTick()
    const videoStudio = wrapper.findComponent({ name: 'VideoStudio' })
    expect(videoStudio.exists()).toBe(true)
    expect(videoStudio.props('catalogLoading')).toBe(true)

    resolvePerKey({
      models: [
        {
          model_id: 'doubao-seedance-1-0-pro-250528',
          billing_mode: 'video',
          your_price: { currency: 'USD', per_second: 0.1088 },
        },
      ],
    })
    await flushPromises()
    expect(videoStudio.props('catalogLoading')).toBe(false)
  })

  it('switches BakeOff to an image-serving key when the child image mode is selected', async () => {
    listKeys.mockResolvedValue({
      items: [
        { id: 1, name: 'trial', key: 'sk-chat', status: 'active', group: { id: 10, name: 'chat' } },
        { id: 2, name: 'imagen', key: 'sk-image', status: 'active', group: { id: 16, name: 'Google-Vertex' } },
      ],
    })
    gatewayListModels.mockImplementation((key: string) => {
      if (key === 'sk-image') return Promise.resolve({ data: [{ id: 'imagen-4.0-generate-001' }] })
      return Promise.resolve({ data: [{ id: 'gpt-5.1' }] })
    })
    getPublicPricing.mockResolvedValue({
      data: [
        {
          model_id: 'imagen-4.0-generate-001',
          pricing: { billing_mode: 'image', output_cost_per_image: 0.04 },
        },
      ],
    })
    getMePricingCatalog.mockImplementation((opts?: { apiKeyId?: number }) => {
      if (opts?.apiKeyId === 2) {
        return Promise.resolve({
          authorized_groups_by_model: {},
          models: [
            {
              model_id: 'imagen-4.0-generate-001',
              billing_mode: 'image',
              your_price: { currency: 'USD', per_image: 0.04 },
            },
          ],
        })
      }
      return Promise.resolve({ authorized_groups_by_model: {}, models: [] })
    })

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })
    await flushPromises()
    await flushPromises()

    await wrapper.find('[data-testid="studio-mode-bakeoff"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="bakeoff-stub-key"]').text()).toBe('sk-chat')

    await wrapper.find('[data-testid="bakeoff-stub-image"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="bakeoff-stub-key"]').text()).toBe('sk-image')
    expect(getMePricingCatalog).toHaveBeenCalledWith({ apiKeyId: 2 })
  })

  it('does not duplicate the route title inside page content', async () => {
    listKeys.mockResolvedValue({
      items: [{ id: 1, name: 'trial', key: 'sk-test', status: 'active', group: { id: 10, name: 'default' } }],
    })

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })
    await flushPromises()

    expect(wrapper.find('h1').exists()).toBe(false)
  })
})
