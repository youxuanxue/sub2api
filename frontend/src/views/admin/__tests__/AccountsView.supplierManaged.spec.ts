import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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
  listSupplierSources,
  duplicateAccount,
  setSchedulable,
  recoverState,
  resetAccountQuota,
  batchClearError,
  showError
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getBatchPassiveUsage: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  getAllIncludingInactive: vi.fn(),
  listEdgeAccounts: vi.fn(),
  listSupplierSources: vi.fn(),
  duplicateAccount: vi.fn(),
  setSchedulable: vi.fn(),
  recoverState: vi.fn(),
  resetAccountQuota: vi.fn(),
  batchClearError: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getBatchPassiveUsage,
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      duplicate: duplicateAccount,
      setSchedulable,
      recoverState,
      resetAccountQuota,
      batchDelete: vi.fn(),
      batchClearError,
      batchRefresh: vi.fn(),
      bulkUpdate: vi.fn()
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups, getAllIncludingInactive },
    edgeAccounts: { listWithEtag: listEdgeAccounts }
  }
}))

vi.mock('@/api/admin/supplierSources', () => ({
  default: { list: listSupplierSources }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
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

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-test="'account-row-' + row.id">
        <div data-test="name"><slot name="cell-name" :row="row" :value="row.name" /></div>
        <div data-test="select"><slot name="cell-select" :row="row" /></div>
        <div data-test="schedulable"><slot name="cell-schedulable" :row="row" /></div>
        <div data-test="actions"><slot name="cell-actions" :row="row" /></div>
      </div>
    </div>
  `
}

const AccountActionMenuStub = {
  props: ['show', 'account'],
  emits: ['duplicate', 'recover-state', 'reset-quota'],
  template: `
    <div v-if="show && account">
      <button data-test="menu-duplicate" @click="$emit('duplicate', account)">duplicate</button>
      <button data-test="menu-recover-state" @click="$emit('recover-state', account)">recover</button>
      <button data-test="menu-reset-quota" @click="$emit('reset-quota', account)">reset quota</button>
    </div>
  `
}

const EditAccountModalStub = {
  props: ['show', 'account'],
  template: '<div data-test="edit-modal" :data-show="String(show)" :data-account-id="account?.id ?? 0" />'
}

const managedAccount = {
  id: 7,
  name: 'managed-account',
  platform: 'newapi',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  supported_protocols: ['chat_completions'],
  extra: { supplier_source_id: 3, supplier_discount_band: 3 },
  priority: 103,
  concurrency: 1,
  created_at: '2026-08-28T00:00:00Z',
  updated_at: '2026-08-28T00:00:00Z'
}

const ordinaryAccount = {
  ...managedAccount,
  id: 8,
  name: 'ordinary-account',
  extra: {}
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="table" /></div>' },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: true,
        AccountActionMenu: AccountActionMenuStub,
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
        EditAccountModal: EditAccountModalStub,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        ChannelTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })
}

describe('admin AccountsView supplier-managed ownership', () => {
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
    listSupplierSources.mockReset()
    duplicateAccount.mockReset()
    setSchedulable.mockReset()
    recoverState.mockReset()
    resetAccountQuota.mockReset()
    batchClearError.mockReset()
    showError.mockReset()

    listAccounts.mockResolvedValue({
      items: [managedAccount, ordinaryAccount],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getBatchPassiveUsage.mockResolvedValue({ usage: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    getAllIncludingInactive.mockResolvedValue([])
    listEdgeAccounts.mockResolvedValue({
      notModified: false,
      etag: null,
      data: { platform: '__by_stub__', edges: [], ts: 1 }
    })
    listSupplierSources.mockResolvedValue([
      {
        id: 3,
        supplier_name: '佳杰',
        channel_name: 'VSTECS',
        endpoint: 'https://example.com/v1',
        base_priority: 100,
        models: [],
        notes: '',
        created_at: '2026-08-28T00:00:00Z',
        updated_at: '2026-08-28T00:00:00Z'
      }
    ])
    recoverState.mockResolvedValue({ ...managedAccount, status: 'active', schedulable: true })
    resetAccountQuota.mockResolvedValue({ ...managedAccount })
    batchClearError.mockResolvedValue({ total: 1, success: 1, failed: 0, errors: [] })
  })

  it('shows the badge and disables row and mixed-selection generic writes', async () => {
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-test="account-row-7"]')
    const ordinaryRow = wrapper.get('[data-test="account-row-8"]')

    expect(managedRow.text()).toContain('admin.accounts.supplierManaged.badge · 佳杰/VSTECS')
    expect(managedRow.get('[data-test="schedulable"] button').attributes('disabled')).toBeDefined()
    expect(managedRow.get('[data-testid="account-edit-btn"]').attributes('disabled')).toBeDefined()
    expect(managedRow.findAll('[data-test="actions"] button')[1].attributes('disabled')).toBeDefined()
    expect(ordinaryRow.get('[data-test="schedulable"] button').attributes('disabled')).toBeUndefined()

    await managedRow.get('[data-test="select"] input').trigger('change')
    await ordinaryRow.get('[data-test="select"] input').trigger('change')

    const bulkDelete = wrapper.findAll('button').find(button =>
      button.text().includes('admin.accounts.bulkActions.delete')
    )
    expect(wrapper.text()).toContain('admin.accounts.supplierManaged.readOnlyReason')
    expect(bulkDelete?.attributes('disabled')).toBeDefined()
  })

  it('rejects a stale child write event before calling the account API', async () => {
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-test="account-row-7"]')
    await managedRow.findAll('[data-test="actions"] button')[2].trigger('click')
    await wrapper.get('[data-test="menu-duplicate"]').trigger('click')

    expect(duplicateAccount).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.accounts.supplierManaged.readOnlyReason')
  })

  it('allows supplier-managed runtime recovery and quota reset actions', async () => {
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-test="account-row-7"]')
    await managedRow.findAll('[data-test="actions"] button')[2].trigger('click')
    await wrapper.get('[data-test="menu-recover-state"]').trigger('click')
    await wrapper.get('[data-test="menu-reset-quota"]').trigger('click')
    await flushPromises()

    expect(recoverState).toHaveBeenCalledWith(7)
    expect(resetAccountQuota).toHaveBeenCalledWith(7)
    expect(showError).not.toHaveBeenCalledWith('admin.accounts.supplierManaged.readOnlyReason')
  })

  it('allows supplier-managed bulk runtime recovery', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-test="account-row-7"]')
    await managedRow.get('[data-test="select"] input').trigger('change')
    const resetStatusButton = wrapper.findAll('button').find(button =>
      button.text().includes('admin.accounts.bulkActions.resetStatus')
    )
    expect(resetStatusButton?.attributes('disabled')).toBeUndefined()

    await resetStatusButton!.trigger('click')
    await flushPromises()

    expect(batchClearError).toHaveBeenCalledWith([7])
    expect(showError).not.toHaveBeenCalledWith('admin.accounts.supplierManaged.readOnlyReason')
  })
})
