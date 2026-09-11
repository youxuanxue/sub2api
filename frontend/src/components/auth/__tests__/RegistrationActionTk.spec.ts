import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import RegistrationActionTk from '../RegistrationActionTk.vue'
import { useAppStore } from '@/stores/app'
import { getPublicSettings } from '@/api/auth'
import type { PublicSettings, RegistrationOffer } from '@/types'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ locale: { value: 'zh' }, t: (key: string, params?: Record<string, string>) => params?.amount ? `${key}:${params.amount}` : key }) }))
vi.mock('@/api/auth', () => ({ getPublicSettings: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAuthenticated: false }) }))
enableAutoUnmount(afterEach)

function settings(offer?: RegistrationOffer) {
  return { registration_enabled: true, signup_bonus_enabled: true, signup_bonus_balance_usd: 100,
    registration_offer: offer, site_name: 'TokenKey' } as PublicSettings
}

async function mountActions() {
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
  ] })
  await router.push('/quickstart')
  const wrapper = mount(defineComponent({
    components: { RegistrationActionTk },
    template: '<div><RegistrationActionTk for-test return-to="/quickstart?client=qwen-code&amp;protocol=openai" /><RegistrationActionTk /></div>',
  }), { global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

describe('RegistrationAction shared policy lifecycle', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.mocked(getPublicSettings).mockReset()
    vi.useFakeTimers()
  })
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

  it('shares one refresh, removes stale credit after failure, and recovers on retry', async () => {
    vi.mocked(getPublicSettings).mockResolvedValue(settings({ state: 'open', signup_bonus_usd: '2.5' }))
    const wrapper = await mountActions()
    expect(getPublicSettings).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('onboarding.bonus:US$2.50')
    expect(wrapper.find('[data-tk="registration-primary"]').attributes('href')).toContain('redirect=/quickstart?client=qwen-code%26protocol=openai')
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(getPublicSettings).mockRejectedValue(new Error('offline'))
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(getPublicSettings).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('US$2.50')
    expect(wrapper.findAll('[role="status"]').map(node => node.text())).toEqual([
      'onboarding.unavailable', 'onboarding.unavailable',
    ])
    expect(useAppStore().cachedPublicSettings?.registration_offer).toEqual({ state: 'unavailable' })
    vi.mocked(getPublicSettings).mockResolvedValue(settings({ state: 'invitation_required' }))
    await wrapper.find('button').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-tk="registration-primary"]').map(node => node.text())).toEqual(['onboarding.invitationRegister', 'onboarding.invitationRegister'])
    expect(wrapper.text()).not.toContain('onboarding.bonus')
  })

  it('never reconstructs a promise from legacy flags when the offer is missing', async () => {
    vi.mocked(getPublicSettings).mockResolvedValue(settings())
    const wrapper = await mountActions()
    expect(wrapper.text()).toContain('onboarding.unavailable')
    expect(wrapper.text()).not.toContain('onboarding.bonus')
    expect(wrapper.find('[data-tk="registration-primary"]').text()).toBe('onboarding.loginAndTest')
    expect(wrapper.find('[data-tk="registration-primary"]').attributes('href')).toContain('/login?')
  })
})
