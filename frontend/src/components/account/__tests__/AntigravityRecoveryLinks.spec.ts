import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import AccountStatusIndicator from '../AccountStatusIndicator.vue'
import GoogleVerificationLink from '../GoogleVerificationLink.vue'
import { accountGoogleVerificationURL, googleVerificationURL } from '@/utils/antigravityRecovery'
import type { Account } from '@/types'

const copy = vi.hoisted(() => vi.fn().mockResolvedValue(true))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copied: ref(false), copyToClipboard: copy }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const url = 'https://accounts.google.com/verify?token=fixture&continue=https%3A%2F%2Fgoogle.com'
const account = {
  id: 24, name: 'anti-fixture', platform: 'antigravity', type: 'oauth',
  status: 'active', schedulable: true,
  temp_unschedulable_reason: `Antigravity validation required temporary cooldown: verify | validation_url: ${url}`,
  temp_unschedulable_until: '2000-01-01T00:00:00Z'
} as Account

describe('Antigravity account recovery links', () => {
  it('opens and copies the exact stored verification URL even after the cooldown has elapsed', async () => {
    const wrapper = mount(AccountStatusIndicator, { props: { account, canReauthorize: true } })
    expect(wrapper.get('a').attributes('href')).toBe(url)
    expect(wrapper.get('a').attributes('rel')).toBe('noopener noreferrer')
    await wrapper.get('[data-testid="google-verification-link"] button').trigger('click')
    expect(copy).toHaveBeenCalledWith(url, 'admin.accounts.linkCopied')
    await wrapper.get('[data-testid="antigravity-authorization-link"]').trigger('click')
    expect(wrapper.emitted('reauth')).toEqual([[account]])
  })

  it('clears the verification action when recovery clears the stored reason', async () => {
    const wrapper = mount(AccountStatusIndicator, { props: { account } })
    expect(wrapper.get('a').attributes('href')).toBe(url)
    await wrapper.setProps({ account: { ...account, temp_unschedulable_reason: null } })
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.find('[data-testid="antigravity-authorization-link"]').exists()).toBe(false)
  })

  it.each([{ platform: 'gemini', type: 'oauth' }, { platform: 'antigravity', type: 'apikey' }, { parent_account_id: 1 }])('does not offer Antigravity OAuth for %j', overrides => {
    const wrapper = mount(AccountStatusIndicator, { props: { account: { ...account, ...overrides } as Account, canReauthorize: true } })
    expect(wrapper.find('[data-testid="antigravity-authorization-link"]').exists()).toBe(false)
  })

  it('supports the legacy permanent error and ignores unrelated platforms', () => {
    expect(accountGoogleVerificationURL({ ...account, temp_unschedulable_reason: '', error_message: `Validation required (403): verify | validation_url: ${url}` })).toBe(url)
    expect(accountGoogleVerificationURL({ ...account, platform: 'openai' })).toBe('')
  })

  it.each(['javascript:alert(1)', 'http://accounts.google.com/verify', 'https://accounts.google.com.evil.test/', 'https://user@accounts.google.com/', 'https://accounts.google.com:8443/', 'not a URL'])('rejects unsafe verification destination %s', value => {
    expect(googleVerificationURL(value)).toBe('')
    const wrapper = mount(GoogleVerificationLink, { props: { url: value } })
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
