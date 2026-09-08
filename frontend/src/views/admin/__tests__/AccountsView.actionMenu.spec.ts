import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'
import AccountActionMenu from '@/components/admin/account/AccountActionMenu.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getBatchPassiveUsage,
  getUpstreamBillingProbeSettings,
  getAllProxies,
  getAllGroups,
  getAllIncludingInactive,
  listEdgeAccounts
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getBatchPassiveUsage: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  getAllIncludingInactive: vi.fn(),
  listEdgeAccounts: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getBatchPassiveUsage,
      getUpstreamBillingProbeSettings,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups, getAllIncludingInactive },
    edgeAccounts: { listWithEtag: listEdgeAccounts }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const ActionDataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" data-test="account-actions">
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}

const account = {
  id: 137,
  name: 'ali-token-plan-3',
  platform: 'newapi',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  priority: 1,
  concurrency: 100,
  channel_type: 17,
  supported_protocols: ['chat_completions', 'responses'],
  extra: {},
  created_at: '2026-09-08T00:00:00Z',
  updated_at: '2026-09-08T00:00:00Z'
}

const mountView = () =>
  mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: ActionDataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
        AccountBulkActionsBar: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        TierTemplatesModal: true,
        AccountTierModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })

async function clickMore(wrapper: ReturnType<typeof mountView>) {
  const more = wrapper.get('[data-testid="account-more-btn"]')
  await more.trigger('click')
  await flushPromises()
  return wrapper.findComponent(AccountActionMenu)
}

describe('admin AccountsView — 行内「更多」菜单', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers({ shouldAdvanceTime: true })
    for (const fn of [
      listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getBatchPassiveUsage,
      getUpstreamBillingProbeSettings,
      getAllProxies,
      getAllGroups,
      getAllIncludingInactive,
      listEdgeAccounts
    ]) {
      fn.mockReset()
    }
    listAccounts.mockResolvedValue({ items: [account], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getBatchPassiveUsage.mockResolvedValue({ usage: {} })
    getUpstreamBillingProbeSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    getAllIncludingInactive.mockResolvedValue([])
    listEdgeAccounts.mockResolvedValue({
      notModified: false,
      etag: null,
      data: { platform: '__by_stub__', edges: [], ts: 1 }
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('点击更多后菜单挂载且 show=true（正向）', async () => {
    const wrapper = mountView()
    await flushPromises()

    const menu = await clickMore(wrapper)
    expect(menu.exists()).toBe(true)
    expect(menu.props('show')).toBe(true)
    expect(menu.props('account')).toMatchObject({ id: 137 })

    wrapper.unmount()
  })

  it('打开后捕获阶段微滚动不立刻关菜单；宽限期后滚动才关闭（负向/回归）', async () => {
    const wrapper = mountView()
    await flushPromises()

    const menu = await clickMore(wrapper)
    expect(menu.props('show')).toBe(true)

    // Nested table overflow scroll during the same click used to close the menu
    // before paint — reproduce with an immediate capture-phase scroll.
    window.dispatchEvent(new Event('scroll', { bubbles: true }))
    await flushPromises()
    expect(menu.props('show')).toBe(true)

    await vi.advanceTimersByTimeAsync(400)
    window.dispatchEvent(new Event('scroll', { bubbles: true }))
    await flushPromises()
    expect(menu.props('show')).toBe(false)

    wrapper.unmount()
  })
})
