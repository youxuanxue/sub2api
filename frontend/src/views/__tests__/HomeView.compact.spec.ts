import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'

import HomeView from '../HomeView.vue'

const { appStore, authStore, profileState } = vi.hoisted(() => ({
  appStore: {
    cachedPublicSettings: {} as Record<string, unknown>,
    siteName: 'Fallback site',
    siteLogo: '',
    docUrl: '',
    publicSettingsLoaded: true,
    fetchPublicSettings: vi.fn(),
  },
  authStore: {
    isAuthenticated: false,
    isAdmin: false,
    user: null as { email?: string } | null,
    checkAuth: vi.fn(),
  },
  profileState: {
    value: 'current' as 'current' | 'china-export',
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/features/home/marketProfile.tk', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/home/marketProfile.tk')>()
  return {
    ...actual,
    resolveHomepageProfile: () => profileState.value,
  }
})

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

function mountHome(settings: Record<string, unknown> = {}) {
  appStore.cachedPublicSettings = {
    site_name: 'Test site',
    site_subtitle: 'Test subtitle',
    ...settings,
  }

  return mount(HomeView, {
    global: {
      stubs: {
        RouterLink: RouterLinkStub,
        LocaleSwitcher: { template: '<div data-testid="locale-switcher" />' },
        Icon: { template: '<span data-testid="icon" />' },
      },
    },
  })
}

function compactDestination(wrapper: ReturnType<typeof mountHome>) {
  return wrapper.get('[data-testid="compact-home"]').findComponent(RouterLinkStub).props('to')
}

function modelPlazaDestination(wrapper: ReturnType<typeof mountHome>) {
  return wrapper
    .findAllComponents(RouterLinkStub)
    .find((link) => link.props('to') === '/model-plaza')
    ?.props('to')
}

describe('HomeView compact mode', () => {
  beforeEach(() => {
    authStore.isAuthenticated = false
    authStore.isAdmin = false
    authStore.user = null
    authStore.checkAuth.mockClear()
    appStore.fetchPublicSettings.mockClear()
    profileState.value = 'current'
    localStorage.clear()
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
  })

  it('renders custom HTML ahead of compact mode', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      home_content: '<section id="custom-home">Custom home</section>',
    })

    expect(wrapper.get('#custom-home').text()).toBe('Custom home')
    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
  })

  it('renders custom URL content ahead of compact mode', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      home_content: ' https://example.com/home ',
    })

    expect(wrapper.get('iframe').attributes('src')).toBe('https://example.com/home')
    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
  })

  it('treats whitespace-only custom content as empty and selects compact mode', () => {
    const wrapper = mountHome({ compact_home_enabled: true, home_content: ' \n\t ' })

    expect(wrapper.get('[data-testid="compact-home"]').text()).toContain('Test site')
  })

  it.each([undefined, false])('selects the default home when compact mode is %s', (enabled) => {
    const settings = enabled === undefined ? {} : { compact_home_enabled: enabled }
    const wrapper = mountHome(settings)

    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
    expect(wrapper.find('.terminal-container').exists()).toBe(true)
  })

  it('links unauthenticated visitors to login', () => {
    expect(compactDestination(mountHome({ compact_home_enabled: true }))).toBe('/login')
  })

  it('links authenticated users to their dashboard', () => {
    authStore.isAuthenticated = true

    expect(compactDestination(mountHome({ compact_home_enabled: true }))).toBe('/dashboard')
  })

  it('links administrators to the admin dashboard', () => {
    authStore.isAuthenticated = true
    authStore.isAdmin = true

    const wrapper = mountHome({ compact_home_enabled: true })
    expect(compactDestination(wrapper)).toBe('/admin/dashboard')
    expect(authStore.checkAuth).toHaveBeenCalledOnce()
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('shows the model plaza link to anonymous visitors when public access is enabled', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: false,
    })

    expect(modelPlazaDestination(wrapper)).toBe('/model-plaza')
  })

  it('hides the model plaza link from anonymous visitors when sign-in is required', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: true,
    })

    expect(modelPlazaDestination(wrapper)).toBeUndefined()
  })

  it('shows the model plaza link to authenticated visitors when sign-in is required', () => {
    authStore.isAuthenticated = true

    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: true,
    })

    expect(modelPlazaDestination(wrapper)).toBe('/model-plaza')
  })

  it('shows the model plaza link in the default home header', () => {
    const wrapper = mountHome({
      model_plaza_enabled: true,
      model_plaza_require_auth: false,
    })

    expect(modelPlazaDestination(wrapper)).toBe('/model-plaza')
  })

  it('hides the model plaza link when the feature is disabled', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: false,
      model_plaza_require_auth: false,
    })

    expect(modelPlazaDestination(wrapper)).toBeUndefined()
  })

  it('ignores custom home content on the China export hostname', () => {
    profileState.value = 'china-export'

    const wrapper = mountHome({ home_content: '<section id="custom-home">Custom home</section>' })

    expect(wrapper.find('#custom-home').exists()).toBe(false)
    expect(wrapper.get('[data-testid="china-export-home"]').exists()).toBe(true)
    expect(wrapper.get('[data-home-profile]').attributes('data-home-profile')).toBe('china-export')
  })

  it('ignores compact mode and internal Model Plaza navigation on the China export hostname', () => {
    profileState.value = 'china-export'

    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: false,
    })

    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
    expect(modelPlazaDestination(wrapper)).toBeUndefined()
    expect(wrapper.get('[data-testid="china-export-home"]').exists()).toBe(true)
  })

  it('keeps the China model order, free trial promise, and current proof media', () => {
    profileState.value = 'china-export'

    const wrapper = mountHome()
    const models = wrapper.get('[data-testid="china-model-list"]').findAll('h3').map((node) => node.text())

    expect(models).toEqual(['Seedance', 'Seedream', 'Qwen', 'DeepSeek', 'GLM', 'Kimi'])
    expect(wrapper.text()).toContain('home.chinaExport.creditDisclaimer')
    expect(wrapper.get('[data-testid="seedance-proof-video"]').attributes('src')).toBe(
      '/seedance-2-5-official-showcase-8b37bc3e.mp4',
    )
    expect(wrapper.get('[data-testid="seedance-proof-video"]').attributes('poster')).toBe(
      '/seedance-2-5-official-poster-db3ff793.jpg',
    )
  })

  it('uses the TokenKey brand and current storefront palette for the China export homepage', () => {
    profileState.value = 'china-export'

    const wrapper = mountHome({ site_name: 'Sub2API' })

    expect(wrapper.get('header').text()).toContain('TokenKey')
    expect(wrapper.get('footer').text()).toContain('TokenKey')
    expect(wrapper.get('footer').text()).not.toContain('Sub2API')
    expect(wrapper.get('[data-home-profile]').classes()).toContain('via-primary-50/30')
    expect(wrapper.get('[data-testid="china-export-primary-cta"]').classes()).toContain('btn-primary')
  })

  it('uses the shared terminal window structure for the China export request', () => {
    profileState.value = 'china-export'

    const terminal = mountHome().get('[data-testid="china-export-terminal"]')

    expect(terminal.get('.terminal-window').exists()).toBe(true)
    expect(terminal.get('.terminal-header').exists()).toBe(true)
    expect(terminal.get('.terminal-body').text()).toContain('deepseek-chat')
    expect(terminal.findAll('.terminal-buttons span')).toHaveLength(3)
  })

  it('sends the primary CTA to the shared product quickstart with DeepSeek selected', () => {
    profileState.value = 'china-export'

    const href = mountHome().get('[data-testid="china-export-primary-cta"]').attributes('href')

    expect(href).toBe(
      'https://tokenkey.dev/register?redirect=%2Fquickstart%3Fmodel%3Ddeepseek-chat%26protocol%3Dopenai',
    )
  })
})
