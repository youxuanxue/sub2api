import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PricingView from '../PricingView.vue'
import type { PublicCatalogResponse } from '@/api/pricing'
import type { MePricingCatalogResponse } from '@/api/me-pricing'

const { getPublicPricing, getMePricingCatalog, authState, exportPricingCsv, showSuccess, showError, routeState, routerPush } =
  vi.hoisted(() => ({
    getPublicPricing: vi.fn(),
    getMePricingCatalog: vi.fn(),
    authState: { isAuthenticated: false, isAdmin: false },
    exportPricingCsv: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn(),
    routeState: { query: {} as Record<string, string | string[] | undefined> },
    routerPush: vi.fn(),
  }))

vi.mock('@/api/pricing', () => ({
  getPublicPricing,
}))

vi.mock('@/api/me-pricing', () => ({
  getMePricingCatalog,
}))

vi.mock('@/composables/useTkPricingExport', () => ({
  exportPricingCsv,
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
    showSuccess,
    showError,
  }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: routerPush }),
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
    'pricing.description': 'Prices are per 1,000 tokens, in USD. Cache columns apply only when billed separately.',
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
    'pricing.search.resultCount': '{count} models',
    'pricing.tableHint': '',
    'pricing.search.noMatches': 'No models match your search',
    'pricing.modality.all': 'All',
    'pricing.modality.text': 'Text',
    'pricing.modality.image': 'Image',
    'pricing.modality.video': 'Video',
    'pricing.filters.apiKey': 'API Key',
    'pricing.filters.keyPlaceholder': 'All keys',
    'pricing.filters.group': 'Group',
    'pricing.filters.publicCatalog': 'All groups',
    'pricing.filters.groupExclusiveOption': '{group} (exclusive)',
    'pricing.filters.search': 'Search',
    'pricing.filters.activePublic': 'Viewing all groups',
    'pricing.filters.activeGroup': 'Viewing {group} group catalog',
    'pricing.filters.activeKeyGroup': 'Viewing {key} · {group}',
    'common.loading': 'Loading',
    'pricing.my.tabMy': 'Group Catalog',
    'pricing.my.tabPublic': 'All groups',
    'pricing.my.title': 'Group Model Catalog',
    'pricing.my.subtitle': '',
    'pricing.my.description': '',
    'pricing.my.pickerKey': 'Current key:',
    'pricing.my.pickerCompare': 'Compare group:',
    'pricing.my.compareDefault': 'Keep current',
    'pricing.my.columns.input': 'Input (official price)',
    'pricing.my.columns.output': 'Output (official price)',
    'pricing.my.empty.noModels.title': 'This group has no models yet',
    'pricing.my.empty.noModels.hint': '',
    'pricing.my.empty.noAccess.title': 'No accessible group',
    'pricing.my.empty.noAccess.hint': '',
    'pricing.my.exploreBanner.message': 'Viewing {group} catalog',
    'pricing.my.exploreBanner.cta': 'Create key in {group}',
    'pricing.my.noKeyHint': '',
    'pricing.perRequest': '/ request',
    'pricing.export.button': 'Export CSV',
    'pricing.export.success': 'Pricing exported',
    'pricing.export.empty': 'Public catalog is empty — nothing to export',
    'pricing.my.columns.authorizedGroups': 'Authorized Groups',
    'pricing.my.authorizedGroups.groupHint': '{group} can serve this model',
    'pricing.my.authorizedGroups.exclusive': 'exclusive',
    'pricing.my.authorizedGroups.quickstart': 'Quick start',
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
        base = base.replace(/\{key\}/g, String(params?.key ?? ''))
        return base
      },
    }),
  }
})

function mountPricingView() {
  return mount(PricingView, {
    global: {
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
        LocaleSwitcher: true,
        Icon: true,
      },
    },
  })
}

function publicCatalog(models: PublicCatalogResponse['data']): PublicCatalogResponse {
  return {
    object: 'list',
    updated_at: '2025-01-01T00:00:00Z',
    data: models,
  }
}

function publicModel(model_id: string): PublicCatalogResponse['data'][number] {
  return {
    model_id,
    vendor: 'openai',
    pricing: {
      currency: 'USD',
      input_per_1k_tokens: 0.001,
      output_per_1k_tokens: 0.002,
    },
    context_window: 128000,
    max_output_tokens: 16384,
    capabilities: [],
  }
}

function meCatalog(overrides: Partial<MePricingCatalogResponse> = {}): MePricingCatalogResponse {
  return {
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
    my_keys: [
      { id: 1, name: 'default', group_id: 10, group_name: 'Pro' },
      { id: 2, name: 'batch', group_id: 20, group_name: 'Batch' },
    ],
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
      {
        id: 20,
        name: 'Batch',
        platform: 'newapi',
        rate_multiplier: 1,
        is_current_for_key: false,
        is_exclusive: false,
        subscription_type: 'standard',
      },
    ],
    updated_at: '2026-05-20T10:00:00Z',
    ...overrides,
  }
}

describe('PricingView', () => {
  beforeEach(() => {
    getPublicPricing.mockReset()
    getMePricingCatalog.mockReset()
    exportPricingCsv.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    routerPush.mockReset()
    localStorage.clear()
    authState.isAuthenticated = false
    authState.isAdmin = false
    routeState.query = {}
  })

  it('prefills exact model search from ?model= deep link', async () => {
    routeState.query = { model: 'gpt-4o-mini' }
    getPublicPricing.mockResolvedValue(
      publicCatalog([publicModel('gpt-4o-mini'), publicModel('gpt-5.4')])
    )

    const wrapper = mountPricingView()
    await flushPromises()

    const search = wrapper.get('#pricing-model-search')
    expect((search.element as HTMLInputElement).value).toBe('gpt-4o-mini')
    expect(wrapper.text()).toContain('gpt-4o-mini')
    expect(wrapper.text()).not.toContain('gpt-5.4')
  })

  it('loads public catalog for ?model= deep link when authenticated', async () => {
    authState.isAuthenticated = true
    routeState.query = { model: 'claude-opus-4-8' }
    getMePricingCatalog.mockResolvedValue(meCatalog())
    getPublicPricing.mockResolvedValue(
      publicCatalog([publicModel('claude-opus-4-8'), publicModel('gpt-5.4')])
    )

    const wrapper = mountPricingView()
    await flushPromises()

    expect(getPublicPricing).toHaveBeenCalled()
    expect(wrapper.text()).toContain('claude-opus-4-8')
    expect(wrapper.text()).not.toContain('gpt-5.4')
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

    const wrapper = mountPricingView()
    await flushPromises()

    expect(wrapper.text()).toContain('Max output')
    expect(wrapper.text()).toContain('16,384')
  })

  it('uses a compact header band instead of a centered hero block', async () => {
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('gpt-4o-mini')]))

    const wrapper = mountPricingView()
    await flushPromises()

    expect(wrapper.find('[data-tk="pricing-page-header"]').exists()).toBe(true)
    expect(wrapper.find('[data-tk="pricing-description-inline"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('Prices are per 1,000 tokens, in USD.')
    expect(wrapper.find('h1').classes().join(' ')).not.toContain('text-3xl')
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

    const wrapper = mountPricingView()
    await flushPromises()

    expect(wrapper.html()).toContain('max-w-[90rem]')
  })

  it('authenticated user defaults to "Group Catalog" view showing official catalog prices', async () => {
    authState.isAuthenticated = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(meCatalog())

    const wrapper = mountPricingView()
    await flushPromises()

    expect(getMePricingCatalog).toHaveBeenCalled()
    expect(getPublicPricing).not.toHaveBeenCalled()
    expect(wrapper.find('[data-tk="pricing-filter-key"]').exists()).toBe(true)
    expect(wrapper.find('[data-tk="pricing-filter-group"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('Viewing default · Pro')
    // Catalog price rendered from your_price (now the official price) — 0.0045 → "$0.0045"
    expect(wrapper.text()).toContain('$0.0045')
    // TK: pricing 页与倍率脱钩——不再展示倍率提示。
    expect(wrapper.text()).not.toContain('Multiplier')
  })

  it('filters the current catalog by search without refetching', async () => {
    getPublicPricing.mockResolvedValue(publicCatalog([
      publicModel('gpt-4o-mini'),
      publicModel('claude-sonnet-4'),
    ]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-filter-search"]').setValue('sonnet')

    expect(wrapper.text()).toContain('claude-sonnet-4')
    expect(wrapper.text()).not.toContain('gpt-4o-mini')
    expect(wrapper.text()).toContain('Showing 1 of 2 models')
    expect(getPublicPricing).toHaveBeenCalledTimes(1)
  })

  it('shows no-match state for a client-side search miss', async () => {
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('gpt-4o-mini')]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-filter-search"]').setValue('missing-model')

    expect(wrapper.text()).toContain('No models match your search')
    expect(wrapper.text()).toContain('0 models')
    expect(getPublicPricing).toHaveBeenCalledTimes(1)
  })

  it('lets authenticated users switch API key, group scope, and public catalog from one toolbar', async () => {
    authState.isAuthenticated = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog
      .mockResolvedValueOnce(meCatalog())
      .mockResolvedValueOnce(meCatalog({
        target_group: {
          ...meCatalog().target_group,
          id: 20,
          name: 'Batch',
        },
        models: [
          {
            ...meCatalog().models[0],
            model_id: 'batch-only-model',
          },
        ],
      }))
      .mockResolvedValueOnce(meCatalog({
        target_group: {
          ...meCatalog().target_group,
          id: 20,
          name: 'Batch',
        },
        models: [
          {
            ...meCatalog().models[0],
            model_id: 'batch-group-model',
          },
        ],
      }))
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('public-model')]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-filter-key"]').setValue('2')
    await flushPromises()

    expect(getMePricingCatalog).toHaveBeenLastCalledWith({ apiKeyId: 2 })
    expect(wrapper.text()).toContain('batch-only-model')

    await wrapper.get('[data-tk="pricing-filter-group"]').setValue('group:20')
    await flushPromises()

    expect(getMePricingCatalog).toHaveBeenLastCalledWith({ groupId: 20 })
    expect(wrapper.text()).toContain('batch-group-model')

    await wrapper.get('[data-tk="pricing-filter-group"]').setValue('public')
    await flushPromises()

    expect(getPublicPricing).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Viewing all groups')
    expect(wrapper.text()).toContain('public-model')
    expect(wrapper.find('[data-tk="pricing-filter-key"]').exists()).toBe(true)
    expect(wrapper.find('[data-tk="pricing-filter-group"]').exists()).toBe(true)
  })

  it('shows authorized groups on the public catalog when logged in', async () => {
    authState.isAuthenticated = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(
      meCatalog({
        authorized_groups_by_model: {
          'public-only-model': [
            {
              id: 10,
              name: 'Pro',
              platform: 'newapi',
              is_exclusive: true,
              is_current_for_key: true,
              rate_multiplier: 1.5,
            },
            {
              id: 20,
              name: 'Batch',
              platform: 'newapi',
              is_exclusive: false,
              is_current_for_key: false,
              rate_multiplier: 1,
            },
          ],
        },
      })
    )
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('public-only-model')]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-filter-group"]').setValue('public')
    await flushPromises()

    expect(wrapper.text()).toContain('Authorized Groups')
    expect(wrapper.text()).toContain('Pro')
    expect(wrapper.text()).toContain('Batch')
    expect(wrapper.text()).toContain('exclusive')
    expect(wrapper.find('[data-tk="pricing-col-authorized-groups"]').exists()).toBe(true)
  })

  it('navigates to quickstart with model when authorized-groups quick start is clicked', async () => {
    authState.isAuthenticated = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(
      meCatalog({
        authorized_groups_by_model: {
          'claude-haiku-4-5': [
            {
              id: 10,
              name: 'claude',
              platform: 'anthropic',
              is_exclusive: true,
              is_current_for_key: true,
              rate_multiplier: 1,
            },
          ],
        },
      })
    )
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('claude-haiku-4-5')]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-filter-group"]').setValue('public')
    await flushPromises()

    await wrapper.get('[data-tk="pricing-quickstart-for-model"]').trigger('click')
    expect(routerPush).toHaveBeenCalledWith({
      path: '/quickstart',
      query: { model: 'claude-haiku-4-5' },
    })
  })

  it('labels exclusive groups in the group filter while viewing the public catalog', async () => {
    authState.isAuthenticated = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(
      meCatalog({
        accessible_groups: [
          {
            id: 10,
            name: 'Pro',
            platform: 'newapi',
            rate_multiplier: 1.5,
            is_current_for_key: true,
            is_exclusive: true,
            subscription_type: 'standard',
          },
          {
            id: 20,
            name: 'Batch',
            platform: 'newapi',
            rate_multiplier: 1,
            is_current_for_key: false,
            is_exclusive: false,
            subscription_type: 'standard',
          },
        ],
      })
    )
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('public-only-model')]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-filter-group"]').setValue('public')
    await flushPromises()

    const groupSelect = wrapper.get('[data-tk="pricing-filter-group"]')
    expect(groupSelect.text()).toContain('Pro (exclusive)')
    expect(groupSelect.text()).toContain('Batch')
    expect(groupSelect.text()).not.toContain('Batch (exclusive)')
  })

  it('hides authorized groups on the public catalog for guests', async () => {
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('public-only-model')]))

    const wrapper = mountPricingView()
    await flushPromises()

    expect(wrapper.text()).not.toContain('Authorized Groups')
  })

  it('falls back to the public catalog when saved auth cannot load the user catalog', async () => {
    localStorage.setItem('auth_token', 'expired-token')
    getMePricingCatalog.mockRejectedValueOnce({ status: 401, message: 'unauthorized' })
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('public-model')]))

    const wrapper = mountPricingView()
    await flushPromises()

    expect(getMePricingCatalog).toHaveBeenCalledTimes(1)
    expect(getPublicPricing).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Viewing all groups')
    expect(wrapper.text()).toContain('public-model')
  })

  it('shows the export button to admins even on a Your-Menu (my) view', async () => {
    authState.isAuthenticated = true
    authState.isAdmin = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(meCatalog())

    const wrapper = mountPricingView()
    await flushPromises()

    // Landed on the my-view (Your-Menu), yet the admin export button is visible.
    expect(wrapper.text()).toContain('Viewing default · Pro')
    expect(wrapper.find('[data-tk="pricing-export-csv"]').exists()).toBe(true)
  })

  it('hides the export button from non-admins', async () => {
    authState.isAuthenticated = true
    authState.isAdmin = false
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(meCatalog())

    const wrapper = mountPricingView()
    await flushPromises()

    expect(wrapper.find('[data-tk="pricing-export-csv"]').exists()).toBe(false)
  })

  it('export from a my-view auto-switches to the public catalog then exports', async () => {
    authState.isAuthenticated = true
    authState.isAdmin = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(meCatalog())
    getPublicPricing.mockResolvedValue(publicCatalog([publicModel('public-model')]))

    const wrapper = mountPricingView()
    await flushPromises()

    // Sanity: started on the my-view and the public catalog was NOT fetched yet.
    expect(wrapper.text()).toContain('Viewing default · Pro')
    expect(getPublicPricing).not.toHaveBeenCalled()

    await wrapper.get('[data-tk="pricing-export-csv"]').trigger('click')
    await flushPromises()

    // Switched to public, loaded it, and exported that catalog.
    expect(getPublicPricing).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Viewing all groups')
    expect(exportPricingCsv).toHaveBeenCalledTimes(1)
    expect(exportPricingCsv.mock.calls[0][0]).toMatchObject({
      data: [expect.objectContaining({ model_id: 'public-model' })],
    })
    expect(showSuccess).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()
  })

  it('export surfaces an error when the public catalog is empty', async () => {
    authState.isAuthenticated = true
    authState.isAdmin = true
    localStorage.setItem('auth_token', 'token')
    getMePricingCatalog.mockResolvedValue(meCatalog())
    getPublicPricing.mockResolvedValue(publicCatalog([]))

    const wrapper = mountPricingView()
    await flushPromises()

    await wrapper.get('[data-tk="pricing-export-csv"]').trigger('click')
    await flushPromises()

    expect(getPublicPricing).toHaveBeenCalledTimes(1)
    expect(exportPricingCsv).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledTimes(1)
  })
})
