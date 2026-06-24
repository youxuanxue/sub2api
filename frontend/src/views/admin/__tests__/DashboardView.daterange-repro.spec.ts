import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import DashboardView from '../DashboardView.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Select from '@/components/common/Select.vue'

// Reproduction harness for the "Token 使用趋势 only shows 2 days" report.
// Mounts the REAL DateRangePicker + Select (the two controls the operator drives)
// and records the start_date/end_date/granularity actually sent to
// /admin/dashboard/snapshot-v2 — instead of guessing from a screenshot.

const { getSnapshotV2, getUserUsageTrend, getUserSpendingRanking } = vi.hoisted(() => ({
  getSnapshotV2: vi.fn(),
  getUserUsageTrend: vi.fn(),
  getUserSpendingRanking: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    dashboard: { getSnapshotV2, getUserUsageTrend, getUserSpendingRanking }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } })
  }
})

const createStats = () =>
  Object.fromEntries(
    [
      'total_users', 'today_new_users', 'active_users', 'hourly_active_users',
      'total_api_keys', 'active_api_keys', 'total_accounts', 'normal_accounts',
      'error_accounts', 'ratelimit_accounts', 'overload_accounts', 'total_requests',
      'total_input_tokens', 'total_output_tokens', 'total_cache_creation_tokens',
      'total_cache_read_tokens', 'total_tokens', 'total_cost', 'total_actual_cost',
      'total_account_cost', 'today_requests', 'today_input_tokens', 'today_output_tokens',
      'today_cache_creation_tokens', 'today_cache_read_tokens', 'today_tokens',
      'today_cost', 'today_actual_cost', 'today_account_cost', 'average_duration_ms',
      'uptime', 'rpm', 'tpm'
    ].map((k) => [k, 0])
  )

const mountDashboard = () =>
  mount(DashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        Icon: true,
        ModelDistributionChart: true,
        TokenUsageTrend: true,
        Line: true
        // NOTE: DateRangePicker + Select are intentionally NOT stubbed.
      }
    }
  })

const lastChartSnapshotCall = () =>
  getSnapshotV2.mock.calls
    .map((call) => call[0])
    .filter((params) => params?.include_trend === true)
    .at(-1) as { start_date: string; end_date: string; granularity: string } | undefined

// open the date picker and click a preset by its (mocked) label key
const clickPreset = async (wrapper: ReturnType<typeof mountDashboard>, labelKey: string) => {
  const dp = wrapper.findComponent(DateRangePicker)
  await dp.find('.date-picker-trigger').trigger('click')
  const presetBtn = dp.findAll('.date-picker-preset').find((b) => b.text() === labelKey)
  if (!presetBtn) throw new Error(`preset not found: ${labelKey}`)
  await presetBtn.trigger('click')
}

// open the granularity Select and click an option by its (mocked) label key
const selectGranularity = async (wrapper: ReturnType<typeof mountDashboard>, labelKey: string) => {
  const sel = wrapper.findComponent(Select)
  await sel.find('button').trigger('click') // opens the (teleported) dropdown
  await flushPromises()
  // Select teleports its panel to document.body, so options live outside the wrapper.
  const opts = Array.from(document.querySelectorAll('.select-option')) as HTMLElement[]
  const opt = opts.find((o) => (o.textContent ?? '').trim().includes(labelKey))
  if (!opt) {
    throw new Error(
      `granularity option not found: ${labelKey}; options=[${opts.map((o) => JSON.stringify(o.textContent)).join(', ')}]`
    )
  }
  opt.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flushPromises()
}

describe('admin DashboardView — date-range / granularity request params', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    getSnapshotV2.mockReset()
    getUserUsageTrend.mockReset()
    getUserSpendingRanking.mockReset()
    getSnapshotV2.mockResolvedValue({
      stats: { ...createStats(), stats_updated_at: '', stats_stale: false },
      trend: [],
      models: []
    })
    getUserUsageTrend.mockResolvedValue({ trend: [] })
    getUserSpendingRanking.mockResolvedValue({
      ranking: [],
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // Regression guard for the screenshot bug: selecting 近14天 used to only flip
  // the trigger label without applying, so a subsequent granularity change still
  // requested the default last-24h window (2 daily buckets) while the label read
  // 近14天. A quick preset must take effect on click.
  it('picking 近14天 preset applies immediately and a later 粒度→按天 keeps the 14-day window', async () => {
    const wrapper = mountDashboard()
    await flushPromises()
    vi.advanceTimersByTime(120)
    await flushPromises()

    // baseline: default last-24h + hour
    expect(lastChartSnapshotCall()).toMatchObject({ granularity: 'hour' })

    await clickPreset(wrapper, 'dates.last14Days')
    await selectGranularity(wrapper, 'admin.dashboard.day')
    await flushPromises()

    const call = lastChartSnapshotCall()!
    const span =
      (new Date(call.end_date).getTime() - new Date(call.start_date).getTime()) / 86400000

    expect(call.granularity).toBe('day')
    expect(span).toBeGreaterThanOrEqual(13) // a real ~14-day window, not 2 buckets
  })

  it('picking 近14天 preset alone sends a real 14-day window', async () => {
    const wrapper = mountDashboard()
    await flushPromises()
    vi.advanceTimersByTime(120)
    await flushPromises()

    await clickPreset(wrapper, 'dates.last14Days')
    await flushPromises()

    const call = lastChartSnapshotCall()!
    const span =
      (new Date(call.end_date).getTime() - new Date(call.start_date).getTime()) / 86400000

    expect(call.granularity).toBe('day')
    expect(span).toBeGreaterThanOrEqual(13)
  })
})
