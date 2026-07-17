import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import UpstreamQuotaSummary from '../UpstreamQuotaSummary.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: { en: {} }
})

describe('UpstreamQuotaSummary hidden credits', () => {
  it('filters duplicate kiro credit lines while keeping the subscription badge', () => {
    const wrapper = mount(UpstreamQuotaSummary, {
      props: {
        quota: {
          provider: 'kiro',
          source: 'passive',
          state: 'observed',
          subscription_tier_raw: 'KIRO POWER',
          credits: [
            {
              key: 'kiro_credits',
              label: 'KIRO POWER',
              current: 729,
              limit: 10000,
              remaining: 9271
            },
            {
              key: 'kiro_trial',
              label: 'Kiro trial',
              current: 5,
              limit: 50,
              remaining: 45
            },
            {
              key: 'kiro_bonus_welcome500',
              label: 'Welcome Bonus',
              current: 120,
              limit: 500,
              remaining: 380
            }
          ]
        },
        hiddenCreditKeyPrefixes: ['kiro_']
      },
      global: {
        plugins: [i18n]
      }
    })

    expect(wrapper.text()).toContain('KIRO POWER')
    expect(wrapper.text()).not.toContain('9.3K/10.0K')
    expect(wrapper.text()).not.toContain('Welcome Bonus')
  })
})
