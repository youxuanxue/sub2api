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
  listEdgeAccounts
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getBatchPassiveUsage: vi.fn(),
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
      getBatchPassiveUsage: getBatchPassiveUsage,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
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
    showError: vi.fn(),
    showSuccess: vi.fn(),
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
        <slot name="cell-created_at" :value="row.created_at" :row="row" />
      </div>
    </div>
  `
}

const AccountBulkActionsBarStub = {
  props: ['selectedIds'],
  emits: ['edit-filtered'],
  template: '<button data-test="edit-filtered" @click="$emit(\'edit-filtered\')">edit filtered</button>'
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
})
