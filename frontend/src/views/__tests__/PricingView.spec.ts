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
  /** Minimal EN strings so assertions match production UI text, not raw keys. */
  const pricingEn: Record<string, string> = {
    'pricing.columns.maxOutput': 'Max output',
    'pricing.footer.total': '{count} models listed',
    'pricing.footer.filtered': 'Showing {shown} of {total} models',
    'pricing.nav.aria': 'Leave pricing page',
    'pricing.nav.home': 'Home',
    'pricing.nav.console': 'Console',
    'pricing.nav.consoleTitleGuest': '',
    'pricing.nav.consoleTitleAuthed': '',
    'pricing.title': 'Model Pricing',
    'pricing.subtitle': '',
    'pricing.description': '',
    'pricing.columns.model': 'Model',
    'pricing.columns.vendor': 'Vendor',
    'pricing.columns.input': 'Input',
    'pricing.columns.output': 'Output',
    'pricing.columns.contextWindow': 'Context window',
    'pricing.columns.capabilities': 'Capabilities',
    'pricing.perThousandTokens': '/ 1K tokens',
    'pricing.updatedAt': 'Last updated {time}',
    'pricing.search.placeholder': '',
    'pricing.search.modeLabel': '',
    'pricing.search.modeFuzzy': '',
    'pricing.search.modeExact': '',
    'pricing.tableHint': '',
    'pricing.search.noMatches': '',
    'common.loading': 'Loading'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        let base = pricingEn[key] ?? key
        base = base.replace(/\{count\}/g, String(params?.count ?? ''))
        base = base.replace(/\{shown\}/g, String(params?.shown ?? ''))
        base = base.replace(/\{total\}/g, String(params?.total ?? ''))
        base = base.replace(/\{time\}/g, String(params?.time ?? ''))
        return base
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

    expect(wrapper.text()).toContain('Max output')
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
