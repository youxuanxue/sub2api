import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { KeepAlive, defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getBatchPassiveUsage,
  getAllProxies,
  getAllGroups,
  getAllIncludingInactive,
  listEdgeAccounts,
  probeUpstreamBilling,
  probeUpstreamBillingBatch,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getBatchPassiveUsage: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  getAllIncludingInactive: vi.fn(),
  listEdgeAccounts: vi.fn(),
  probeUpstreamBilling: vi.fn(),
  probeUpstreamBillingBatch: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getBatchPassiveUsage: getBatchPassiveUsage,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      probeUpstreamBilling,
      probeUpstreamBillingBatch,
      toggleSchedulable: vi.fn()
    },
    proxies: {
      getAll: getAllProxies
    },
    groups: {
      getAll: getAllGroups,
      getAllIncludingInactive
    },
    edgeAccounts: {
      listWithEtag: listEdgeAccounts
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token'
  })
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

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <span v-for="column in columns" :key="column.key" data-test="column-key">{{ column.key }}</span>
      <div v-for="row in data" :key="row.id">
        <div data-test="select-row"><slot name="cell-select" :row="row" /></div>
        <slot name="cell-created_at" :value="row.created_at" :row="row" />
        <div data-test="account-rate"><slot name="cell-rate_multiplier" :row="row" /></div>
      </div>
    </div>
  `
}

const ProbeDataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id">
        <div data-test="account-rate"><slot name="cell-rate_multiplier" :row="row" /></div>
        <slot name="cell-upstream_billing_rate" :row="row" />
      </div>
    </div>
  `
}

const AccountBulkActionsBarStub = {
  props: ['selectedIds'],
  emits: ['edit-filtered', 'probe-upstream-billing'],
  template: `
    <div>
      <button data-test="edit-filtered" @click="$emit('edit-filtered')">edit filtered</button>
      <button data-test="probe-upstream-billing" @click="$emit('probe-upstream-billing')">probe</button>
    </div>
  `
}

const PaginationStub = {
  emits: ['update:page'],
  template: '<button data-test="next-page" @click="$emit(\'update:page\', 2)">next</button>'
}

const AccountTableActionsStub = {
  emits: ['refresh', 'sync', 'create'],
  template: `
    <div>
      <button data-test="refresh-accounts" @click="$emit('refresh')">refresh</button>
      <slot name="beforeCreate" />
      <slot name="after" />
    </div>
  `
}

const BulkEditAccountModalStub = {
  props: ['show', 'target'],
  template: '<div data-test="bulk-edit-modal" :data-show="String(show)" :data-target-mode="target?.mode ?? \'\'"></div>'
}

const AccountTableFiltersStub = {
  props: ['groups'],
  template: '<div data-test="account-group-options">{{ groups.map(group => group.name).join(",") }}</div>'
}

const accountsViewStubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: {
    template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
  },
  DataTable: DataTableStub,
  Pagination: true,
  ConfirmDialog: true,
  AccountTableActions: AccountTableActionsStub,
  AccountTableFilters: AccountTableFiltersStub,
  AccountBulkActionsBar: AccountBulkActionsBarStub,
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
  CreateAccountModal: true,
  EditAccountModal: true,
  BulkEditAccountModal: BulkEditAccountModalStub,
  PlatformTypeBadge: true,
  AccountCapacityCell: true,
  AccountStatusIndicator: true,
  AccountTodayStatsCell: true,
  AccountGroupsCell: true,
  AccountUsageCell: true,
  Icon: true
}

const mountAccountsView = () => mount(AccountsView, {
  global: { stubs: accountsViewStubs }
})

const InactiveView = defineComponent({
  name: 'InactiveView',
  setup: () => () => h('div')
})

const CachedAccountsHost = defineComponent({
  props: {
    showAccounts: { type: Boolean, required: true }
  },
  setup(props) {
    return () => h(KeepAlive, null, {
      default: () => props.showAccounts
        ? h(AccountsView, { key: 'accounts' })
        : h(InactiveView, { key: 'inactive' })
    })
  }
})

describe('admin AccountsView bulk edit scope', () => {
  beforeEach(() => {
    localStorage.clear()

    listAccounts.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getBatchPassiveUsage.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    getAllIncludingInactive.mockReset()
    listEdgeAccounts.mockReset()
    probeUpstreamBilling.mockReset()
    probeUpstreamBillingBatch.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listAccounts.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getBatchPassiveUsage.mockResolvedValue({ usage: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    getAllIncludingInactive.mockResolvedValue([])
    listEdgeAccounts.mockResolvedValue({ notModified: false, etag: null, data: { platform: '__by_stub__', edges: [], ts: 1 } })
    probeUpstreamBilling.mockResolvedValue({})
    probeUpstreamBillingBatch.mockResolvedValue([])
  })

  it('opens bulk edit in filtered-results mode from the bulk actions dropdown', async () => {
    const wrapper = mountAccountsView()

    await flushPromises()
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-show')).toBe('true')
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-target-mode')).toBe('filtered')
  })

  it('reloads account group choices during a manual refresh', async () => {
    getAllGroups
      .mockResolvedValueOnce([{ id: 19, name: 'china', platform: 'newapi' }])
      .mockResolvedValueOnce([
        { id: 19, name: 'china', platform: 'newapi' },
        { id: 285, name: 'Kimi', platform: 'newapi' }
      ])
    getAllIncludingInactive
      .mockResolvedValueOnce([{ id: 19, name: 'china', platform: 'newapi' }])
      .mockResolvedValueOnce([
        { id: 19, name: 'china', platform: 'newapi' },
        { id: 285, name: 'Kimi', platform: 'newapi' }
      ])

    const wrapper = mountAccountsView()

    await flushPromises()
    expect(getAllGroups).toHaveBeenCalledTimes(1)
    expect(getAllIncludingInactive).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="account-group-options"]').text()).not.toContain('Kimi')

    await wrapper.get('[data-test="refresh-accounts"]').trigger('click')
    await flushPromises()

    expect(getAllGroups).toHaveBeenCalledTimes(2)
    expect(getAllIncludingInactive).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="account-group-options"]').text()).toContain('Kimi')
  })

  it('reloads account group choices when the kept-alive page is reactivated', async () => {
    getAllGroups
      .mockResolvedValueOnce([{ id: 19, name: 'china', platform: 'newapi' }])
      .mockResolvedValueOnce([
        { id: 19, name: 'china', platform: 'newapi' },
        { id: 285, name: 'Kimi', platform: 'newapi' }
      ])
    getAllIncludingInactive
      .mockResolvedValueOnce([{ id: 19, name: 'china', platform: 'newapi' }])
      .mockResolvedValueOnce([
        { id: 19, name: 'china', platform: 'newapi' },
        { id: 285, name: 'Kimi', platform: 'newapi' }
      ])

    const wrapper = mount(CachedAccountsHost, {
      props: { showAccounts: true },
      global: { stubs: accountsViewStubs }
    })

    await flushPromises()
    expect(getAllGroups).toHaveBeenCalledTimes(1)
    expect(getAllIncludingInactive).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ showAccounts: false })
    await flushPromises()
    await wrapper.setProps({ showAccounts: true })
    await flushPromises()

    expect(getAllGroups).toHaveBeenCalledTimes(2)
    expect(getAllIncludingInactive).toHaveBeenCalledTimes(2)
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="account-group-options"]').text()).toContain('Kimi')
  })

  it('uses the compact account operation columns by default', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'test-account',
          platform: 'anthropic',
          type: 'oauth',
          status: 'active',
          schedulable: true,
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        }
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mountAccountsView()

    await flushPromises()

    const columnKeys = wrapper.findAll('[data-test="column-key"]').map(node => node.text())
    expect(columnKeys).toEqual([
      'select',
      'name',
      'platform_type',
      'capacity',
      'status',
      'schedulable',
      'groups',
      'usage',
      'priority',
      'actions'
    ])
    expect(columnKeys).not.toContain('id')
    expect(columnKeys).not.toContain('today_stats')
    expect(columnKeys).not.toContain('created_at')
    const columns = wrapper.getComponent(DataTableStub).props('columns') as Array<{ key: string; label: string; sortable: boolean }>
    expect(columns.find(column => column.key === 'priority')).toMatchObject({
      label: 'admin.accounts.columns.priority',
      sortable: true
    })
  })

  it('migrates the old auto-saved default hidden columns to the compact default', async () => {
    localStorage.setItem(
      'account-hidden-columns',
      JSON.stringify(['today_stats', 'proxy', 'notes', 'priority', 'rate_multiplier'])
    )
    listAccounts.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })

    const wrapper = mountAccountsView()

    await flushPromises()

    const columnKeys = wrapper.findAll('[data-test="column-key"]').map(node => node.text())
    expect(columnKeys).toEqual([
      'select',
      'name',
      'platform_type',
      'capacity',
      'status',
      'schedulable',
      'groups',
      'usage',
      'priority',
      'actions'
    ])
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).toEqual([
      'id',
      'today_stats',
      'proxy',
      'scheduler_score',
      'rate_multiplier',
      'last_used_at',
      'created_at',
      'expires_at',
      'notes'
    ])
    expect(localStorage.getItem('account-column-settings-version')).toBe('3')
  })

  it('manual refresh also force-refreshes inline edge panels', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 69,
          name: 'kiro-us4',
          platform: 'anthropic',
          type: 'apikey',
          status: 'active',
          schedulable: true,
          edge_id: 'us4',
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        }
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mountAccountsView()

    await flushPromises()
    listEdgeAccounts.mockClear()
    await wrapper.get('[data-test="refresh-accounts"]').trigger('click')
    await flushPromises()

    expect(listEdgeAccounts).toHaveBeenCalledWith({ view: 'by-stub' }, { force: true })
  })

  it('refreshes the current page after a batch probe and displays the synced rate', async () => {
    const account = (id: number, rateMultiplier: number) => ({
      id,
      name: `account-${id}`,
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      rate_multiplier: rateMultiplier,
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    })
    listAccounts
      .mockResolvedValueOnce({ items: [account(7, 0.25)], total: 2, page: 1, page_size: 1, pages: 2 })
      .mockResolvedValueOnce({ items: [account(11, 0.25)], total: 2, page: 2, page_size: 1, pages: 2 })
      .mockResolvedValueOnce({ items: [account(11, 0.065)], total: 2, page: 2, page_size: 1, pages: 2 })
    probeUpstreamBillingBatch.mockResolvedValue([
      {
        account_id: 11,
        snapshot: {
          status: 'ok',
          data: { effective_rate_multiplier: 0.065 },
          last_attempt_at: '2026-07-13T00:00:00Z',
          next_probe_at: '2026-07-13T00:30:00Z'
        }
      }
    ])

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /><slot name="pagination" /></div>' },
          DataTable: DataTableStub,
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountActionMenu: true,
          Pagination: PaginationStub,
          ConfirmDialog: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
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

    await flushPromises()
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="probe-upstream-billing"]').trigger('click')
    await flushPromises()

    expect(probeUpstreamBillingBatch).toHaveBeenCalledWith([11])
    expect(listAccounts).toHaveBeenCalledTimes(3)
    expect(listAccounts.mock.calls[2]?.[0]).toBe(2)
    expect(wrapper.get('[data-test="account-rate"]').text()).toBe('0.065x')
  })

  it('does not report a successful batch probe as failed when the list refresh fails', async () => {
    const account = {
      id: 7,
      name: 'account-7',
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      rate_multiplier: 0.25,
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    }
    listAccounts
      .mockResolvedValueOnce({ items: [account], total: 1, page: 1, page_size: 20, pages: 1 })
      .mockRejectedValueOnce(new Error('refresh failed'))
    probeUpstreamBillingBatch.mockResolvedValue([
      {
        account_id: 7,
        snapshot: {
          status: 'ok',
          data: { effective_rate_multiplier: 0.065 },
          last_attempt_at: '2026-07-13T00:00:00Z',
          next_probe_at: '2026-07-13T00:30:00Z'
        }
      }
    ])
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: DataTableStub,
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountActionMenu: true,
          Pagination: true,
          ConfirmDialog: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
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

    await flushPromises()
    await wrapper.get('[data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="probe-upstream-billing"]').trigger('click')
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.upstreamBilling.batchCompleted')
    consoleError.mockRestore()
  })

  it('refreshes the account row after a successful single-account probe', async () => {
    const account = (rateMultiplier: number) => ({
      id: 7,
      name: 'account-7',
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      rate_multiplier: rateMultiplier,
      extra: { upstream_billing_probe_enabled: true },
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    })
    listAccounts
      .mockResolvedValueOnce({ items: [account(0.25)], total: 1, page: 1, page_size: 20, pages: 1 })
      .mockResolvedValueOnce({ items: [account(0.065)], total: 1, page: 1, page_size: 20, pages: 1 })
    probeUpstreamBilling.mockResolvedValue({
      account_id: 7,
      snapshot: {
        status: 'ok',
        data: { effective_rate_multiplier: 0.065 },
        last_attempt_at: '2026-07-13T00:00:00Z',
        next_probe_at: '2026-07-13T00:30:00Z'
      }
    })

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: ProbeDataTableStub,
          AccountBulkActionsBar: true,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountActionMenu: true,
          Pagination: true,
          ConfirmDialog: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
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

    await flushPromises()
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()

    expect(probeUpstreamBilling).toHaveBeenCalledWith(7)
    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="account-rate"]').text()).toBe('0.065x')
  })
})
