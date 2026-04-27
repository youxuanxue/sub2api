import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PricingView from '../PricingView.vue'
import type { PublicCatalogResponse } from '@/api/pricing'

const { getPublicPricing } = vi.hoisted(() => ({
  getPublicPricing: vi.fn(),
}))

vi.mock('@/api/pricing', () => ({
  getPublicPricing,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    isAuthenticated: false,
    isAdmin: false,
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: {
      pricing_catalog_public: true,
      registration_enabled: false,
      signup_bonus_enabled: false,
      signup_bonus_balance_usd: 0,
      backend_mode_enabled: false,
    },
    fetchPublicSettings: vi.fn(),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'pricing.footer.total' && params && typeof params.count === 'number') {
          return `${params.count} models`
        }
        return key
      },
    }),
  }
})

describe('PricingView', () => {
  beforeEach(() => {
    getPublicPricing.mockReset()
  })

  it('shows max output column when catalog includes max_output_tokens', async () => {
    const catalog: PublicCatalogResponse = {
      object: 'list',
      updated_at: '2025-01-01T00:00:00Z',
      data: [
        {
          model_id: 'gpt-test',
          vendor: 'openai',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0.001,
            output_per_1k_tokens: 0.002,
          },
          context_window: 128000,
          max_output_tokens: 16384,
          capabilities: [],
        },
      ],
    }
    getPublicPricing.mockResolvedValue(catalog)

    const wrapper = mount(PricingView, {
      global: {
        stubs: {
          RouterLink: { template: '<a><slot /></a>' },
          LocaleSwitcher: true,
          Icon: true,
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('pricing.columns.maxOutput')
    expect(wrapper.text()).toContain('16,384')
  })

  it('uses wider layout wrapper (full catalog width)', async () => {
    const catalog: PublicCatalogResponse = {
      object: 'list',
      updated_at: '2025-01-01T00:00:00Z',
      data: [
        {
          model_id: 'x',
          pricing: {
            currency: 'USD',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
          },
          capabilities: [],
        },
      ],
    }
    getPublicPricing.mockResolvedValue(catalog)

    const wrapper = mount(PricingView, {
      global: {
        stubs: {
          RouterLink: { template: '<a><slot /></a>' },
          LocaleSwitcher: true,
          Icon: true,
        },
      },
    })
    await flushPromises()

    expect(wrapper.html()).toContain('max-w-[90rem]')
  })
})
