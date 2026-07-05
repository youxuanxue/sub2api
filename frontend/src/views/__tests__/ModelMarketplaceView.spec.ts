import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ModelMarketplaceView from '../ModelMarketplaceView.vue'
import type { PublicCatalogModel, PublicCatalogResponse } from '@/api/pricing'

const { getPublicPricing, authState } = vi.hoisted(() => ({
  getPublicPricing: vi.fn(),
  authState: { isAuthenticated: false },
}))

vi.mock('@/api/pricing', () => ({
  getPublicPricing,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const modelsEn: Record<string, string> = {
    'models.title': 'Model Marketplace',
    'models.subtitle': 'Browse models',
    'models.filterAll': 'All',
    'models.filterText': 'Text',
    'models.filterImage': 'Image',
    'models.filterVideo': 'Video',
    'models.searchPlaceholder': 'Search models...',
    'models.noModels': 'No models match your filters.',
    'models.providers': 'Providers',
    'models.inputPrice': 'Input',
    'models.outputPrice': 'Output',
    'models.pricePerK': '/ 1K tokens',
    'models.viewPricing': 'View Pricing Details',
    'models.capabilities.image_generation': 'Image generation',
    'models.capabilities.video_generation': 'Video generation',
    'pricing.nav.home': 'Home',
    'nav.quickstart': 'Quick Start',
    'auth.createAccount': 'Create account',
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => modelsEn[key] ?? key,
      te: (key: string) => key in modelsEn,
    }),
  }
})

function catalog(models: PublicCatalogModel[]): PublicCatalogResponse {
  return { object: 'list', updated_at: '2025-01-01T00:00:00Z', data: models }
}

function model(
  model_id: string,
  vendor: string,
  capabilities: string[] = []
): PublicCatalogModel {
  return {
    model_id,
    vendor,
    capabilities,
    pricing: {
      currency: 'USD',
      input_per_1k_tokens: 0.001,
      output_per_1k_tokens: 0.002,
    },
  }
}

function mountMarketplace() {
  return mount(ModelMarketplaceView, {
    global: {
      stubs: {
        RouterLink: { props: ['to'], template: '<a><slot /></a>' },
      },
    },
  })
}

describe('ModelMarketplaceView', () => {
  beforeEach(() => {
    getPublicPricing.mockReset()
    authState.isAuthenticated = false
  })

  it('renders image models when image filter is selected', async () => {
    getPublicPricing.mockResolvedValue(
      catalog([
        model('gpt-4o-mini', 'OpenAI'),
        model('imagen-4.0-generate-001', 'Google', ['image_generation']),
      ])
    )

    const wrapper = mountMarketplace()
    await flushPromises()

    expect(wrapper.text()).toContain('gpt-4o-mini')
    expect(wrapper.text()).toContain('imagen-4.0-generate-001')

    const imageBtn = wrapper
      .findAll('button')
      .find((b) => b.text().includes('Image'))
    expect(imageBtn).toBeTruthy()
    await imageBtn!.trigger('click')

    expect(wrapper.text()).toContain('imagen-4.0-generate-001')
    expect(wrapper.text()).not.toContain('gpt-4o-mini')
    expect(wrapper.text()).toContain('Image generation')
  })

  it('shows empty state when search matches no models', async () => {
    getPublicPricing.mockResolvedValue(catalog([model('gpt-4o-mini', 'OpenAI')]))

    const wrapper = mountMarketplace()
    await flushPromises()

    const search = wrapper.get('input[type="text"]')
    await search.setValue('does-not-exist')
    await flushPromises()

    expect(wrapper.text()).toContain('No models match your filters.')
    expect(wrapper.text()).not.toContain('gpt-4o-mini')
  })
})
