import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PricingView from '../PricingView.vue'
import type { PublicCatalogResponse } from '@/api/pricing'
import type { MePricingCatalogResponse } from '@/api/me-pricing'

const { getPublicPricing, getMePricingCatalog, authState } = vi.hoisted(() => ({
  getPublicPricing: vi.fn(),
  getMePricingCatalog: vi.fn(),
  authState: { isAuthenticated: false, isAdmin: false },
}))

vi.mock('@/api/pricing', () => ({
  getPublicPricing,
}))

vi.mock('@/api/me-pricing', () => ({
  getMePricingCatalog,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState,
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
    'common.loading': 'Loading',
    'pricing.my.tabMy': 'Your Menu',
    'pricing.my.tabPublic': 'Public Catalog',
    'pricing.my.title': 'Your Model Menu',
    'pricing.my.subtitle': '',
    'pricing.my.description': '',
    'pricing.my.pickerKey': 'Current key:',
    'pricing.my.pickerCompare': 'Compare group:',
    'pricing.my.compareDefault': 'Keep current',
    'pricing.my.columns.input': 'Input (your price)',
    'pricing.my.columns.output': 'Output (your price)',
    'pricing.my.rateHint': 'Multiplier {multiplier} applied',
    'pricing.my.rateOverride': 'includes personal override',
    'pricing.my.empty.noModels.title': 'This group has no models yet',
    'pricing.my.empty.noModels.hint': '',
    'pricing.my.empty.noAccess.title': 'No accessible group',
    'pricing.my.empty.noAccess.hint': '',
    'pricing.my.exploreBanner.message': 'Viewing {group} catalog · ×{multiplier}',
    'pricing.my.exploreBanner.cta': 'Create key in {group}',
    'pricing.my.noKeyHint': '',
    'pricing.perRequest': '/ request'
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
        base = base.replace(/\{multiplier\}/g, String(params?.multiplier ?? ''))
        base = base.replace(/\{group\}/g, String(params?.group ?? ''))
        return base
      },
    }),
  }
})

describe('PricingView', () => {
  beforeEach(() => {
    getPublicPricing.mockReset()
    getMePricingCatalog.mockReset()
    authState.isAuthenticated = false
    authState.isAdmin = false
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

  it('authenticated user defaults to "Your Menu" view with your_price applied', async () => {
    authState.isAuthenticated = true
    const me: MePricingCatalogResponse = {
      target_group: {
        id: 10,
        name: 'Pro',
        platform: 'newapi',
        rate_multiplier: 1.5,
        list_multiplier: 1.5,
        has_override: false,
        is_exclusive: false,
        subscription_type: 'standard',
      },
      models: [
        {
          model_id: 'gpt-4o',
          vendor: 'openai',
          billing_mode: 'token',
          your_price: {
            currency: 'USD',
            input_per_1k: 0.0045,
            output_per_1k: 0.0225,
          },
          context_window: 128000,
          max_output_tokens: 16384,
          capabilities: ['vision'],
        },
      ],
      my_keys: [{ id: 1, name: 'default', group_id: 10, group_name: 'Pro' }],
      accessible_groups: [
        {
          id: 10,
          name: 'Pro',
          platform: 'newapi',
          rate_multiplier: 1.5,
          is_current_for_key: true,
          is_exclusive: false,
          subscription_type: 'standard',
        },
      ],
      updated_at: '2026-05-20T10:00:00Z',
    }
    getMePricingCatalog.mockResolvedValue(me)

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

    expect(getMePricingCatalog).toHaveBeenCalled()
    expect(getPublicPricing).not.toHaveBeenCalled()
    // Hero swaps to "Your Model Menu" copy.
    expect(wrapper.text()).toContain('Your Model Menu')
    // your_price applied — input 0.0045 → "$0.0045"
    expect(wrapper.text()).toContain('$0.0045')
    // Rate hint shown when multiplier != 1.
    expect(wrapper.text()).toContain('Multiplier ×1.5 applied')
  })
})
