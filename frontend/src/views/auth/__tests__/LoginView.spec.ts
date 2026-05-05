import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

const {
  pushMock,
  loginMock,
  showSuccessMock,
  showErrorMock,
  showWarningMock,
  getPublicSettingsMock,
} = vi.hoisted(() => ({
  pushMock: vi.fn(),
  loginMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  showWarningMock: vi.fn(),
  getPublicSettingsMock: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: pushMock,
    currentRoute: { value: { query: {} } },
  }),
}))

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key,
    },
  }),
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    login: (...args: any[]) => loginMock(...args),
    login2FA: vi.fn(),
  }),
  useAppStore: () => ({
    showSuccess: (...args: any[]) => showSuccessMock(...args),
    showError: (...args: any[]) => showErrorMock(...args),
    showWarning: (...args: any[]) => showWarningMock(...args),
  }),
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    getPublicSettings: (...args: any[]) => getPublicSettingsMock(...args),
    isTotp2FARequired: (response: any) => response?.requires_2fa === true,
    isWeChatWebOAuthEnabled: () => false,
  }
})

describe('LoginView', () => {
  beforeEach(() => {
    pushMock.mockReset()
    loginMock.mockReset()
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
    showWarningMock.mockReset()
    getPublicSettingsMock.mockReset()
    sessionStorage.clear()
    localStorage.clear()

    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      backend_mode_enabled: false,
      oidc_oauth_enabled: false,
      password_reset_enabled: true,
    })
    loginMock.mockResolvedValue({
      access_token: 'token',
      token_type: 'Bearer',
      user: { id: 1, email: 'user@example.com' },
    })
  })

  it('does not render or submit Turnstile on login even when public settings enable it', async () => {
    const wrapper = mount(LoginView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
          LinuxDoOAuthSection: true,
          OidcOAuthSection: true,
          WechatOAuthSection: true,
          TotpLoginModal: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    expect(wrapper.findComponent({ name: 'TurnstileWidget' }).exists()).toBe(false)

    await wrapper.find('#email').setValue('user@example.com')
    await wrapper.find('#password').setValue('password123')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(loginMock).toHaveBeenCalledWith({
      email: 'user@example.com',
      password: 'password123',
    })
  })
})
