import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'

import CatalogHubShell from '@/components/catalog/CatalogHubShell.vue'
import CatalogViewSwitcher from '@/components/catalog/CatalogViewSwitcher.vue'

const { authState, routeState } = vi.hoisted(() => ({
  authState: { isAuthenticated: false },
  routeState: { path: '/models', query: {} as Record<string, string | string[] | undefined> },
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState,
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div data-test="app-layout"><slot /></div>' },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
}))

describe('CatalogViewSwitcher', () => {
  beforeEach(() => {
    routeState.path = '/models'
    routeState.query = {}
  })

  it('marks browse active on /models without view query', () => {
    const wrapper = mount(CatalogViewSwitcher, {
      global: {
        stubs: {
          RouterLink: {
            props: ['to'],
            template: '<a :data-to="JSON.stringify(to)"><slot /></a>',
          },
        },
      },
    })
    expect(wrapper.get('[data-tk="catalog-view-browse"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.get('[data-tk="catalog-view-pricing"]').attributes('aria-selected')).toBe('false')
    expect(wrapper.get('[data-tk="catalog-view-browse"]').attributes('data-to')).toBe('{"path":"/models"}')
    expect(wrapper.get('[data-tk="catalog-view-pricing"]').attributes('data-to')).toBe(
      JSON.stringify({ path: '/models', query: { view: 'pricing' } }),
    )
  })

  it('marks pricing active on /models?view=pricing', () => {
    routeState.query = { view: 'pricing' }
    const wrapper = mount(CatalogViewSwitcher, {
      global: {
        stubs: {
          RouterLink: {
            props: ['to'],
            template: '<a :data-to="JSON.stringify(to)"><slot /></a>',
          },
        },
      },
    })
    expect(wrapper.get('[data-tk="catalog-view-browse"]').attributes('aria-selected')).toBe('false')
    expect(wrapper.get('[data-tk="catalog-view-pricing"]').attributes('aria-selected')).toBe('true')
  })
})

describe('CatalogHubShell', () => {
  beforeEach(() => {
    authState.isAuthenticated = false
  })

  it('uses AppLayout when authenticated', () => {
    authState.isAuthenticated = true
    const wrapper = mount(CatalogHubShell, {
      props: { authedDataTk: 'catalog-authed' },
      slots: { default: '<p data-test="body">content</p>' },
    })
    expect(wrapper.find('[data-test="app-layout"]').exists()).toBe(true)
    expect(wrapper.find('[data-tk="catalog-authed"]').exists()).toBe(true)
  })

  it('renders guest chrome slot when logged out', () => {
    const wrapper = mount(CatalogHubShell, {
      slots: {
        'guest-chrome': '<header data-test="guest-header">guest</header>',
        default: '<p data-test="body">content</p>',
      },
    })
    expect(wrapper.find('[data-test="guest-header"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="app-layout"]').exists()).toBe(false)
  })
})
