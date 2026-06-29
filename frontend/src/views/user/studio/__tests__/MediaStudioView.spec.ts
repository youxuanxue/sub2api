import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { ref } from 'vue'
import en from '@/i18n/locales/en'
import MediaStudioView from '../MediaStudioView.vue'

const { listKeys, gatewayListModels, getMePricingCatalog, getPublicPricing } = vi.hoisted(() => ({
  listKeys: vi.fn(),
  gatewayListModels: vi.fn(),
  getMePricingCatalog: vi.fn(),
  getPublicPricing: vi.fn(),
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
    user: ref({ id: 1, balance: 100 }),
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
vi.mock('../VideoStudio.vue', () => ({ default: { template: '<div />' } }))
vi.mock('../BakeOff.vue', () => ({ default: { template: '<div />' } }))

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
    fetchPublicSettings.mockClear()

    listKeys.mockResolvedValue({
      items: [{ id: 1, name: 'trial', key: 'sk-test', group: { id: 10, name: 'default' } }],
    })
    gatewayListModels.mockResolvedValue({ data: [{ id: 'gpt-4o' }] })
    getPublicPricing.mockResolvedValue({ data: [] })
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

  it('loads entitlement index for universal keys without per-key pricing catalog', async () => {
    listKeys.mockResolvedValue({
      items: [{ id: 9, name: 'universal', key: 'sk-uni', routing_mode: 'universal', group_id: null }],
    })
    getMePricingCatalog.mockResolvedValue({
      authorized_groups_by_model: {
        'veo-3.1-generate-001': [{ group_id: 3, group_name: 'vertex' }],
      },
      models: [],
    })
    getPublicPricing.mockResolvedValue({
      data: [{ model_id: 'veo-3.1-generate-001', pricing: { output_cost_per_second: 0.5 } }],
    })
    gatewayListModels.mockResolvedValue({ data: [] })

    const wrapper = mount(MediaStudioView, {
      global: { plugins: [i18n], stubs: { 'router-link': true } },
    })
    await flushPromises()
    await flushPromises()

    expect(getMePricingCatalog).toHaveBeenCalled()
    expect(getPublicPricing).toHaveBeenCalled()
    expect(wrapper.text()).toContain('studio.universalKeyBadge')
    expect(getMePricingCatalog).not.toHaveBeenCalledWith(expect.objectContaining({ apiKeyId: 9 }))
  })
})
