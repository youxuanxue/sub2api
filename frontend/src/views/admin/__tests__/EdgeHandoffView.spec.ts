import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import EdgeHandoffView from '@/views/admin/EdgeHandoffView.vue'

const {
  locationState,
  routerReplaceMock,
  setTokenMock,
  persistOAuthTokenContextMock,
  replaceStateMock,
} = vi.hoisted(() => ({
  locationState: {
    current: { pathname: '/admin/edge-handoff', search: '', hash: '' },
  },
  routerReplaceMock: vi.fn(),
  setTokenMock: vi.fn(),
  persistOAuthTokenContextMock: vi.fn(),
  replaceStateMock: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: (...args: any[]) => routerReplaceMock(...args) }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ setToken: (...args: any[]) => setTokenMock(...args) }),
}))

vi.mock('@/api/auth', () => ({
  persistOAuthTokenContext: (...args: any[]) => persistOAuthTokenContextMock(...args),
}))

describe('EdgeHandoffView', () => {
  beforeEach(() => {
    locationState.current = { pathname: '/admin/edge-handoff', search: '', hash: '' }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState.current,
    })
    Object.defineProperty(window, 'history', {
      configurable: true,
      value: { replaceState: (...args: any[]) => replaceStateMock(...args) },
    })
    routerReplaceMock.mockReset()
    setTokenMock.mockReset().mockResolvedValue({})
    persistOAuthTokenContextMock.mockReset()
    replaceStateMock.mockReset()
  })

  it('persists refresh_token + expires_in before setToken so the session self-renews', async () => {
    locationState.current.hash =
      '#tk_session=jwt.access.value&refresh_token=rt_value&expires_in=3600&next=/admin/accounts'

    mount(EdgeHandoffView)
    await flushPromises()

    expect(persistOAuthTokenContextMock).toHaveBeenCalledWith({
      refresh_token: 'rt_value',
      expires_in: 3600,
    })
    expect(setTokenMock).toHaveBeenCalledWith('jwt.access.value')
    // refresh persistence must precede setToken (setToken reads it from storage).
    expect(persistOAuthTokenContextMock.mock.invocationCallOrder[0]).toBeLessThan(
      setTokenMock.mock.invocationCallOrder[0],
    )
    expect(routerReplaceMock).toHaveBeenCalledWith('/admin/accounts')
    // The fragment (token + refresh_token) is scrubbed from the address bar.
    expect(replaceStateMock).toHaveBeenCalledWith(null, '', '/admin/edge-handoff')
  })

  it('renders error state and skips setToken when no token is present', async () => {
    locationState.current.hash = '#next=/admin/accounts'

    const wrapper = mount(EdgeHandoffView)
    await flushPromises()

    expect(setTokenMock).not.toHaveBeenCalled()
    expect(persistOAuthTokenContextMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.edgeAccounts.handoff.failed')
  })
})
