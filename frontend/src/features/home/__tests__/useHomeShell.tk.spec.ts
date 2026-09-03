import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'

import { useHomeShell } from '../useHomeShell.tk'

const { appStore, authStore } = vi.hoisted(() => ({
  appStore: {
    cachedPublicSettings: {} as Record<string, unknown>,
    docUrl: '',
    siteLogo: '',
    siteName: 'TokenKey',
  },
  authStore: {
    checkAuth: vi.fn(),
    isAdmin: false,
    isAuthenticated: false,
    user: null,
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
}))

const HomeShellProbe = defineComponent({
  setup() {
    const { docUrl } = useHomeShell()
    return { docUrl }
  },
  template: '<a v-if="docUrl" data-testid="docs" :href="docUrl">Docs</a>',
})

describe('useHomeShell doc URL', () => {
  beforeEach(() => {
    appStore.cachedPublicSettings = {}
    appStore.docUrl = ''
    authStore.checkAuth.mockClear()
    localStorage.clear()
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
  })

  it('keeps a valid HTTPS documentation URL', () => {
    appStore.cachedPublicSettings = { doc_url: ' https://docs.tokenkey.dev/start ' }

    const wrapper = mount(HomeShellProbe)

    expect(wrapper.get('[data-testid="docs"]').attributes('href')).toBe(
      'https://docs.tokenkey.dev/start',
    )
  })

  it('removes an unsafe documentation URL from the shared home shell', () => {
    appStore.cachedPublicSettings = { doc_url: 'javascript:alert(1)' }

    const wrapper = mount(HomeShellProbe)

    expect(wrapper.find('[data-testid="docs"]').exists()).toBe(false)
  })
})
