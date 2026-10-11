import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import ApiKeyDistributionChart from '../ApiKeyDistributionChart.vue'

const messages: Record<string, string> = {
  'usage.apiKeyDistribution': 'API Key Distribution',
  'usage.apiKeyFilter': 'API Key',
  'usage.cost': 'Cost',
  'usage.days7': '7d',
  'usage.days30': '30d',
  'usage.unknownApiKey': 'Unknown key',
  'admin.dashboard.requests': 'Requests',
  'admin.dashboard.tokens': 'Tokens',
  'admin.dashboard.metricTokens': 'By Tokens',
  'admin.dashboard.metricActualCost': 'By Actual Cost',
  'admin.dashboard.noDataAvailable': 'No data available',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Doughnut: {
    props: ['data'],
    template: '<div class="chart-data">{{ JSON.stringify(data) }}</div>',
  },
}))

describe('ApiKeyDistributionChart', () => {
  const apiKeyStats = [
    {
      api_key_id: 1,
      api_key_name: 'prod-key',
      requests: 9,
      total_tokens: 1200,
      cost: 1.8,
      actual_cost: 0.1,
    },
    {
      api_key_id: 2,
      api_key_name: 'dev-key',
      requests: 4,
      total_tokens: 600,
      cost: 0.7,
      actual_cost: 0.9,
    },
  ]

  it('orders and charts by tokens by default', () => {
    const wrapper = mount(ApiKeyDistributionChart, {
      props: { apiKeyStats },
      global: { stubs: { LoadingSpinner: true } },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['prod-key', 'dev-key'])
    expect(chartData.datasets[0].data).toEqual([1200, 600])

    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('prod-key')
    expect(rows[1].text()).toContain('dev-key')
  })

  it('reorders by actual cost when metric changes', async () => {
    const wrapper = mount(ApiKeyDistributionChart, {
      props: { apiKeyStats, metric: 'actual_cost' },
      global: { stubs: { LoadingSpinner: true } },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['dev-key', 'prod-key'])
    expect(chartData.datasets[0].data).toEqual([0.9, 0.1])
  })

  it('emits days tab changes', async () => {
    const wrapper = mount(ApiKeyDistributionChart, {
      props: { apiKeyStats, days: 7 },
      global: { stubs: { LoadingSpinner: true } },
    })

    await wrapper.get('[data-testid="api-key-distribution-days-30"]').trigger('click')
    expect(wrapper.emitted('update:days')?.[0]).toEqual([30])
  })
})
