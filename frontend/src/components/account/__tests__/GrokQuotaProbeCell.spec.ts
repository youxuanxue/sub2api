import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GrokQuotaProbeCell from '../GrokQuotaProbeCell.vue'
import type { Account, AccountUsageInfo } from '@/types'
import UsageProgressBar from '../UsageProgressBar.vue'

const { getUsage } = vi.hoisted(() => ({
  getUsage: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { getUsage: getUsage }
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
    getUsage.mockReset()
  })

  it('keeps billing data while exposing a usage error without inference probing', async () => {
    getUsage.mockResolvedValue({
      source: 'active',
      grok_billing: { period_type: 'weekly', usage_percent: null },
      error: 'upstream quota unavailable'
    })
    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('upstream quota unavailable')
    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(getUsage).toHaveBeenCalledWith(99, 'active', true)
    wrapper.unmount()
  })

  it('uses queried billing and matching windows, independently of stale account tier', async () => {
    getUsage.mockResolvedValue({
      grok_billing: {
        plan: 'SuperGrok Lite', period_type: 'weekly', usage_percent: 37,
        period_end: '2026-09-17T00:00:00Z',
        monthly_limit_cents: 2500, used_cents: 350,
      },
      grok_local_usage_7d: { requests: 7, tokens: 2200, cost: 2 },
      grok_local_usage_monthly: { requests: 30, tokens: 8800, cost: 8 },
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

    getUsage.mockResolvedValue({
      grok_billing: { monthly_limit: 0, monthly_used: 0, prepaid_balance: 12.5 },
      grok_local_usage_24h: { requests: 4, tokens: 750000, cost: 1 },
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
    expect(getUsage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('adopts a refreshed parent snapshot after a manual query', async () => {
    getUsage.mockResolvedValue({ grok_billing: { prepaid_balance: 12.5 } })
    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('$12.50')

    await wrapper.setProps({ usage: { grok_billing: { prepaid_balance: 9 } } as AccountUsageInfo })
    expect(wrapper.text()).toContain('$9.00')
    expect(wrapper.text()).not.toContain('$12.50')
    expect(getUsage).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it.each(['refresh', 'account'] as const)('ignores a late query after %s changes', async change => {
    let resolve!: (result: { grok_billing: { prepaid_balance: number } }) => void
    getUsage.mockReturnValue(new Promise(result => { resolve = result }))
    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })
    await wrapper.get('button').trigger('click')
    await wrapper.setProps({
      ...(change === 'account' ? { account: { ...account, id: 100 } } : {}),
      usage: { grok_billing: { prepaid_balance: 9 } } as AccountUsageInfo,
    })
    resolve({ grok_billing: { prepaid_balance: 12.5 } })
    await flushPromises()
    expect(wrapper.text()).toContain('$9.00')
    expect(wrapper.text()).not.toContain('$12.50')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('bounds edge queries to explicit clicks and never falls back to a main account ID', async () => {
    let reject!: (error: Error) => void
    const activeUsageLoader = vi.fn().mockReturnValue(new Promise((_, fail) => { reject = fail }))
    const wrapper = mount(GrokQuotaProbeCell, {
      props: { account, activeUsageLoader, usage: { grok_billing: { prepaid_balance: 9 } } as AccountUsageInfo },
    })
    expect(activeUsageLoader).not.toHaveBeenCalled()
    await wrapper.get('button').trigger('click')
    await wrapper.get('button').trigger('click')
    expect(activeUsageLoader).toHaveBeenCalledTimes(1)
    reject(new Error('edge unavailable'))
    await flushPromises()
    expect(getUsage).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('edge unavailable')
    expect(wrapper.text()).toContain('$9.00')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})
