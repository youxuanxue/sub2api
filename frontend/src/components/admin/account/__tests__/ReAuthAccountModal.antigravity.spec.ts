import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { expect, it, vi } from 'vitest'
import ReAuthAccountModal from '../ReAuthAccountModal.vue'
import OAuthAuthorizationFlow from '@/components/account/OAuthAuthorizationFlow.vue'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/api/admin', () => ({ adminAPI: { antigravity: { generateAuthUrl: vi.fn() } } }))

it('keeps the pending OAuth session during metadata refresh and regenerates only when the account changes', async () => {
  vi.mocked(adminAPI.antigravity.generateAuthUrl)
    .mockResolvedValueOnce({ auth_url: 'https://accounts.google.com/auth?state=first', session_id: 'first', state: 'first' })
    .mockResolvedValueOnce({ auth_url: 'https://accounts.google.com/auth?state=second', session_id: 'second', state: 'second' })
  const account = { id: 24, name: 'anti-fixture', platform: 'antigravity', type: 'oauth', proxy_id: 7 } as Account
  const wrapper = shallowMount(ReAuthAccountModal, {
    props: { show: true, account, autoGenerate: true },
    global: { plugins: [createPinia()], stubs: { BaseDialog: { template: '<div><slot /></div>' }, OAuthAuthorizationFlow: { name: 'OAuthAuthorizationFlow', props: ['authUrl', 'sessionId'], methods: { reset: vi.fn() }, template: '<div />' } } }
  })
  try {
    await flushPromises()
    expect(wrapper.getComponent(OAuthAuthorizationFlow).props('sessionId')).toBe('first')
    await wrapper.setProps({ account: { ...account, name: 'updated metadata' } })
    await flushPromises()
    expect(wrapper.getComponent(OAuthAuthorizationFlow).props('sessionId')).toBe('first')
    expect(adminAPI.antigravity.generateAuthUrl).toHaveBeenCalledTimes(1)
    await wrapper.setProps({ account: { ...account, id: 25, proxy_id: 9 } })
    await flushPromises()
    expect(wrapper.getComponent(OAuthAuthorizationFlow).props('sessionId')).toBe('second')
    expect(adminAPI.antigravity.generateAuthUrl).toHaveBeenLastCalledWith({ proxy_id: 9 })
  } finally {
    wrapper.unmount()
  }
})
