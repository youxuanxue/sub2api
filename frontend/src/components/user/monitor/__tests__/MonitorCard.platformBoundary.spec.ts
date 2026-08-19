import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorQuotaVisible: () => false,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, te: () => true }),
  }
})

import MonitorCard from '../MonitorCard.vue'

describe('MonitorCard public platform boundary', () => {
  it('hides Antigravity as Google in the legacy user monitor card', () => {
    const wrapper = mount(MonitorCard, {
      props: {
        publicPlatformNames: true,
        item: {
          id: 1,
          name: 'Antigravity channel',
          provider: 'antigravity',
          group_name: 'default',
          primary_model: 'gemini-2.5-pro',
          primary_status: 'operational',
          primary_latency_ms: 10,
          primary_ping_latency_ms: 12,
          availability_7d: 99,
          extra_models: [],
          timeline: [],
        },
        window: '7d',
        availabilityValue: 99,
        countdownSeconds: 10,
      },
      global: {
        stubs: {
          ProviderIcon: true,
          MonitorMetricPair: true,
          MonitorAvailabilityRow: true,
          MonitorTimeline: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Google')
    expect(wrapper.text()).not.toContain('antigravity')
  })
})
