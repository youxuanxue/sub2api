import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

import GroupBadge from '../GroupBadge.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  messages: {
    en: {
      groups: { subscription: 'Subscription' },
      admin: {
        users: {
          expired: 'Expired',
          daysRemaining: '{days} days',
        },
      },
    },
  },
})

function mountBadge(props: Record<string, unknown>) {
  return mount(GroupBadge, {
    props: {
      name: 'Demo Group',
      platform: 'newapi',
      subscriptionType: 'standard',
      ...props,
    },
    global: { plugins: [i18n] },
  })
}

describe('GroupBadge (newapi visual parity)', () => {
  it('renders newapi standard badge in cyan palette', () => {
    const wrapper = mountBadge({ rateMultiplier: 1.5 })
    const html = wrapper.html()
    expect(html).toContain('bg-cyan-50')
    expect(html).toContain('text-cyan-700')
    expect(html).not.toContain('bg-violet-100')
  })

  it('renders newapi subscription badge in cyan palette', () => {
    const wrapper = mountBadge({
      subscriptionType: 'subscription',
      daysRemaining: 15,
    })
    const html = wrapper.html()
    expect(html).toContain('bg-cyan-100')
    expect(html).toContain('text-cyan-700')
    expect(html).not.toContain('bg-violet-100')
  })
})
