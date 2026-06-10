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

// TK: 用户级页面隐藏倍率（hideRateValue）。倍率数值一律不展示，但订阅
// 「订阅/天数」标签保留（那不是倍率）。
describe('GroupBadge (hideRateValue — user-facing pages)', () => {
  it('hides the standard rate label when hideRateValue', () => {
    const wrapper = mountBadge({ rateMultiplier: 1.5, hideRateValue: true })
    expect(wrapper.text()).not.toContain('1.5x')
  })

  it('hides the 1x default rate too', () => {
    const wrapper = mountBadge({ rateMultiplier: 1, hideRateValue: true })
    expect(wrapper.text()).not.toContain('1x')
  })

  it('hides the struck-through per-user override when hideRateValue', () => {
    const wrapper = mountBadge({
      rateMultiplier: 2,
      userRateMultiplier: 0.5,
      hideRateValue: true,
    })
    const text = wrapper.text()
    expect(text).not.toContain('2x')
    expect(text).not.toContain('0.5x')
  })

  it('still shows the standard rate when hideRateValue is not set', () => {
    const wrapper = mountBadge({ rateMultiplier: 1.5 })
    expect(wrapper.text()).toContain('1.5x')
  })

  it('keeps the subscription days label even when hideRateValue (not a rate)', () => {
    // alwaysShowRate would normally force the rate; hideRateValue must fall the
    // subscription badge back to its days label and drop the rate value.
    // (i18n interpolation is disabled in this test build, so the days label
    // renders as the raw key — we assert the rate is gone + the label exists.)
    const wrapper = mountBadge({
      subscriptionType: 'subscription',
      daysRemaining: 15,
      alwaysShowRate: true,
      rateMultiplier: 3,
      hideRateValue: true,
    })
    const text = wrapper.text()
    expect(text).not.toContain('3x')
    expect(text).toContain('daysRemaining')
  })
})
