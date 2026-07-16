import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { DashboardStats, GroupStat } from '@/types'
import DashboardView from '../DashboardView.vue'

const { getSnapshotV2, getUserUsageTrend, getUserSpendingRanking, routerPush } = vi.hoisted(() => ({
  getSnapshotV2: vi.fn(),
  getUserUsageTrend: vi.fn(),
  getUserSpendingRanking: vi.fn(),
  routerPush: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    dashboard: {
      getSnapshotV2,
      getUserUsageTrend,
      getUserSpendingRanking
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: routerPush
  })
}))

vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({
    canUseBatchImage: { value: false },
    refreshBatchImageAccess: vi.fn()
  })
}))

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'Line',
    props: ['data', 'options'],
    template: '<div data-test="line-chart"></div>'
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

const createDashboardStats = (): DashboardStats => ({
  total_users: 0,
  today_new_users: 0,
  active_users: 0,
  hourly_active_users: 0,
  stats_updated_at: '',
  stats_stale: false,
  total_api_keys: 0,
  active_api_keys: 0,
  total_accounts: 0,
  normal_accounts: 0,
  error_accounts: 0,
  ratelimit_accounts: 0,
  overload_accounts: 0,
  total_requests: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0,
  total_tokens: 0,
  total_cost: 0,
  total_actual_cost: 0,
  total_account_cost: 0,
  today_requests: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 0,
  today_cost: 0,
  today_actual_cost: 0,
  today_account_cost: 0,
  average_duration_ms: 0,
  uptime: 0,
  rpm: 0,
  tpm: 0
})

const createGroupStat = (overrides: Partial<GroupStat>): GroupStat => ({
  group_id: 1,
  group_name: 'group-1',
  requests: 1,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  cache_telemetry_unavailable_input_tokens: 0,
  total_tokens: 0,
  cost: 0,
  actual_cost: 0,
  account_cost: 0,
  ...overrides
})

const mountDashboard = () =>
  mount(DashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        Icon: true,
        DateRangePicker: true,
        Select: true,
        ModelDistributionChart: true,
        TokenUsageTrend: true,
        Line: true
      }
    }
  })

describe('admin DashboardView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    getSnapshotV2.mockReset()
    getUserUsageTrend.mockReset()
    getUserSpendingRanking.mockReset()
    routerPush.mockReset()

    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: [],
      groups: []
    })
    getUserUsageTrend.mockResolvedValue({
      trend: [],
      start_date: '',
      end_date: '',
      granularity: 'hour'
    })
    getUserSpendingRanking.mockResolvedValue({
      ranking: [],
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0,
      start_date: '',
      end_date: ''
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('uses last 24 hours as default dashboard range', async () => {
    mountDashboard()

    await flushPromises()

    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      start_date: formatLocalDate(yesterday),
      end_date: formatLocalDate(now),
      granularity: 'hour',
      include_stats: true,
      include_trend: true,
      include_model_stats: true,
      include_group_stats: true,
      include_users_trend: true,
      users_trend_limit: 12
    }))

    vi.advanceTimersByTime(120)
    await flushPromises()

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getUserSpendingRanking).toHaveBeenCalledTimes(1)
  })

  it('shows the observable rate and the five highest-impact groups', async () => {
    vi.setSystemTime(new Date('2026-07-16T08:43:21Z'))
    const endTs = Date.UTC(2026, 6, 16, 8, 43, 0)
    const startTs = endTs - 24 * 60 * 60 * 1000
    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: [],
      groups: [
        createGroupStat({ group_id: 6, group_name: 'sixth', input_tokens: 10, total_tokens: 10 }),
        createGroupStat({ group_id: 2, group_name: 'kiro', input_tokens: 800, cache_telemetry_unavailable_input_tokens: 800, total_tokens: 800 }),
        createGroupStat({ group_id: 4, group_name: 'fourth', input_tokens: 100, cache_read_tokens: 100, total_tokens: 200 }),
        createGroupStat({ group_id: 1, group_name: 'largest', input_tokens: 600, cache_read_tokens: 400, total_tokens: 1000 }),
        createGroupStat({ group_id: 5, group_name: 'fifth', input_tokens: 100, total_tokens: 100 }),
        createGroupStat({ group_id: 3, group_name: 'partial', input_tokens: 300, cache_read_tokens: 100, cache_telemetry_unavailable_input_tokens: 200, total_tokens: 400 })
      ]
    })

    const wrapper = mountDashboard()
    await flushPromises()

    expect(wrapper.get('[data-testid="prompt-cache-card"] [data-testid="prompt-cache-rate"]').text()).toBe('39.7%')
    const rows = wrapper.findAll('[data-testid="prompt-cache-group-row"]')
    expect(rows).toHaveLength(5)
    expect(rows.map((row) => row.attributes('data-group-id'))).toEqual(['1', '2', '3', '4', '5'])
    expect(rows[1].get('[data-testid="prompt-cache-status"]').text()).toBe('admin.dashboard.promptCacheUnavailable')
    expect(rows[1].text()).not.toContain('0.0%')
    expect(rows[2].get('[data-testid="prompt-cache-status"]').text()).toBe('admin.dashboard.promptCachePartiallyObservable')
    expect(rows[2].get('[data-testid="prompt-cache-rate"]').text()).toBe('50.0%')

    await rows[0].trigger('click')
    expect(routerPush).toHaveBeenCalledWith(expect.objectContaining({
      path: '/admin/usage',
      query: expect.objectContaining({ group_id: '1', start_ts: startTs, end_ts: endTs })
    }))
  })

  it('shows unavailable instead of zero when all prompt traffic lacks cache telemetry', async () => {
    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: [],
      groups: [createGroupStat({
        group_id: 9,
        group_name: 'kiro-only',
        input_tokens: 900,
        cache_telemetry_unavailable_input_tokens: 900,
        total_tokens: 900
      })]
    })

    const wrapper = mountDashboard()
    await flushPromises()

    const card = wrapper.get('[data-testid="prompt-cache-card"]')
    expect(card.get('[data-testid="prompt-cache-status"]').text()).toBe('admin.dashboard.promptCacheUnavailable')
    expect(card.text()).not.toContain('0.0%')
  })

  it('uses DOM legend buttons for recent usage and toggles the matching dataset', async () => {
    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: [],
      users_trend: [
        {
          date: '2026-07-10',
          user_id: 1,
          username: 'alice',
          email: 'alice@example.com',
          requests: 1,
          tokens: 100,
          cost: 0,
          actual_cost: 0
        },
        {
          date: '2026-07-10',
          user_id: 2,
          username: '',
          email: 'bob@example.com',
          requests: 1,
          tokens: 200,
          cost: 0,
          actual_cost: 0
        }
      ]
    })

    const wrapper = mountDashboard()
    await flushPromises()

    const legend = wrapper.get('[data-test="recent-usage-legend"]')
    const buttons = legend.findAll('button')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].text()).toContain('alice')
    expect(buttons[1].text()).toContain('bob@example.com')

    const line = wrapper.getComponent({ name: 'Line' })
    expect(line.props('options').plugins.legend.display).toBe(false)
    expect(line.props('data').datasets.map((dataset: any) => dataset.hidden)).toEqual([
      false,
      false
    ])

    await buttons[1].trigger('click')
    await flushPromises()

    expect(buttons[1].attributes('aria-pressed')).toBe('false')
    expect(wrapper.getComponent({ name: 'Line' }).props('data').datasets.map((dataset: any) => dataset.hidden)).toEqual([
      false,
      true
    ])
  })
})
