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
    'pricing.perImage': '/ image',
    'pricing.perSecond': '/ second',
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
  overrides: Partial<PublicCatalogModel> = {}
): PublicCatalogModel {
  return {
    model_id,
    vendor,
    capabilities: [],
    pricing: {
      currency: 'USD',
      input_per_1k_tokens: 0.001,
      output_per_1k_tokens: 0.002,
    },
    ...overrides,
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

  it('filters image models by pricing.billing_mode, not capabilities tags', async () => {
    getPublicPricing.mockResolvedValue(
      catalog([
        model('gpt-4o-mini', 'OpenAI'),
        model('imagen-4.0-generate-001', 'Google', {
          pricing: {
            currency: 'USD',
            billing_mode: 'image',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
            output_cost_per_image: 0.04,
          },
        }),
      ])
    )

    const wrapper = mountMarketplace()
    await flushPromises()

    const imageBtn = wrapper.findAll('button').find((b) => b.text().includes('Image'))
    expect(imageBtn).toBeTruthy()
    await imageBtn!.trigger('click')

    expect(wrapper.text()).toContain('imagen-4.0-generate-001')
    expect(wrapper.text()).not.toContain('gpt-4o-mini')
  })

  it('filters video models by pricing.billing_mode', async () => {
    getPublicPricing.mockResolvedValue(
      catalog([
        model('gpt-4o-mini', 'OpenAI'),
        model('veo-3.1-generate-preview', 'Google', {
          pricing: {
            currency: 'USD',
            billing_mode: 'video',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
            output_cost_per_second: 0.5,
          },
        }),
      ])
    )

    const wrapper = mountMarketplace()
    await flushPromises()

    const videoBtn = wrapper.findAll('button').find((b) => b.text().includes('Video'))
    expect(videoBtn).toBeTruthy()
    await videoBtn!.trigger('click')

    expect(wrapper.text()).toContain('veo-3.1-generate-preview')
    expect(wrapper.text()).not.toContain('gpt-4o-mini')
  })

  it('shows media unit pricing instead of treating zero token prices as free', async () => {
    getPublicPricing.mockResolvedValue(
      catalog([
        model('doubao-seedream-5-0-260128', 'volcengine', {
          pricing: {
            currency: 'USD',
            billing_mode: 'image',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
            output_cost_per_image: 0.03480597014925373,
          },
        }),
        model('doubao-seedance-2-0-fast-260128', 'volcengine', {
          pricing: {
            currency: 'USD',
            billing_mode: 'video',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
            output_cost_per_second: 0.1265671641791045,
          },
        }),
        model('future-image-with-missing-price', 'volcengine', {
          pricing: {
            currency: 'USD',
            billing_mode: 'image',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
          },
        }),
      ])
    )

    const wrapper = mountMarketplace()
    await flushPromises()

    expect(wrapper.text()).toContain('$0.0348 / image')
    expect(wrapper.text()).toContain('$0.1266 / second')
    expect(wrapper.text()).toContain('— / image')
    expect(wrapper.text()).not.toContain('Free')
    expect(wrapper.text()).not.toContain('/ 1K tokens')
  })

  it('groups all Vertex provider variants under vertex_ai', async () => {
    getPublicPricing.mockResolvedValue(
      catalog([
        model('gemini-2.5-pro', 'vertex_ai-language-models'),
        model('imagen-4.0-generate-001', 'vertex_ai', {
          pricing: {
            currency: 'USD',
            billing_mode: 'image',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
            output_cost_per_image: 0.04,
          },
        }),
        model('veo-3.1-generate-001', 'vertex_ai-video-models', {
          pricing: {
            currency: 'USD',
            billing_mode: 'video',
            input_per_1k_tokens: 0,
            output_per_1k_tokens: 0,
            output_cost_per_second: 0.6,
          },
        }),
      ])
    )

    const wrapper = mountMarketplace()
    await flushPromises()

    const vertexButtons = wrapper
      .findAll('button')
      .filter((button) => button.text().trim() === 'vertex_ai (3)')
    expect(vertexButtons).toHaveLength(2)
    expect(wrapper.findAll('button').some((button) => button.text().includes('vertex_ai-language-models'))).toBe(false)

    await vertexButtons[0].trigger('click')
    expect(wrapper.text()).toContain('gemini-2.5-pro')
    expect(wrapper.text()).toContain('imagen-4.0-generate-001')
    expect(wrapper.text()).toContain('veo-3.1-generate-001')
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
