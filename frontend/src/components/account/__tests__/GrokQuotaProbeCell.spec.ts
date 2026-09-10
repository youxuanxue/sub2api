import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GrokQuotaProbeCell from '../GrokQuotaProbeCell.vue'
import type { Account, AccountUsageInfo } from '@/types'
import UsageProgressBar from '../UsageProgressBar.vue'

const { queryQuota } = vi.hoisted(() => ({
  queryQuota: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: { queryQuota }
  }
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params?.percent == null ? key : `${key}:${params.percent}`
  })
}))

const account = {
  id: 99,
  platform: 'grok',
  type: 'oauth'
} as Account

describe('GrokQuotaProbeCell', () => {
  beforeEach(() => {
    queryQuota.mockReset()
  })

  it('keeps billing data while exposing a failed Free quota fallback', async () => {
    queryQuota.mockResolvedValue({
      source: 'hybrid_probe',
      billing: { period_type: 'weekly', usage_percent: null },
      headers_observed: false,
      reset_supported: false,
      fetched_at: 1,
      probe_error: 'upstream returned 402 for probe model "grok-4.5"'
    })
    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('upstream returned 402 for probe model "grok-4.5"')
    expect(wrapper.emitted('probed')?.[0]?.[0]).toMatchObject({
      billing: { period_type: 'weekly', usage_percent: null },
      probe_error: 'upstream returned 402 for probe model "grok-4.5"'
    })
    wrapper.unmount()
  })

  it('uses queried billing and matching windows, independently of stale account tier', async () => {
    queryQuota.mockResolvedValue({
      billing: {
        plan: 'SuperGrok Lite', period_type: 'weekly', usage_percent: 37,
        period_end: '2026-09-17T00:00:00Z',
        monthly_limit_cents: 2500, used_cents: 350,
      },
      local_usage_7d: { requests: 7, tokens: 2200, cost: 2 },
      local_usage_monthly: { requests: 30, tokens: 8800, cost: 8 },
    })
    const wrapper = mount(GrokQuotaProbeCell, {
      props: { account: { ...account, credentials: { subscription_tier: 'free' } } }
    })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    const bar = wrapper.getComponent(UsageProgressBar)
    expect(bar.props()).toMatchObject({ label: '7d', utilization: 37, windowStats: { tokens: 2200 } })
    expect(wrapper.text()).toContain('$3.50 / $25.00')
    expect(wrapper.text()).toContain('8.8K tok')
    expect(wrapper.text()).not.toContain('grokPrepaid')

    queryQuota.mockResolvedValue({
      billing: { monthly_limit: 0, monthly_used: 0, prepaid_balance: 12.5 },
      local_usage_24h: { requests: 4, tokens: 750000, cost: 1 },
    })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('grokPrepaid: $12.50')
    expect(wrapper.text()).toContain('24h4 req750.0K tok')
    expect(wrapper.findComponent(UsageProgressBar).exists()).toBe(false)
    expect(wrapper.text()).not.toContain('grokMonthlyLimit')
    wrapper.unmount()
  })

  it('does not infer a Free quota or rolling 24h from today statistics', async () => {
    const wrapper = mount(GrokQuotaProbeCell, {
      props: {
        account,
        usage: {
          grok_local_usage: { requests: 4, tokens: 1_000_000, cost: 0 },
          grok_billing: { prepaid_balance: 0 },
        } as AccountUsageInfo
      }
    })
    expect(wrapper.text()).toContain('grokPrepaid: $0.00')
    expect(wrapper.find('[data-testid="usage-stats-row"]').exists()).toBe(false)
    expect(wrapper.findComponent(UsageProgressBar).exists()).toBe(false)
    expect(queryQuota).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
