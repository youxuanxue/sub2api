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
  priority: 130,
  concurrency: 1,
  group_ids: [],
  extra: { supplier_source_id: 7, supplier_discount_band: 3 },
  created_at: '2026-08-28T00:00:00Z',
  updated_at: '2026-08-28T00:00:00Z'
}

const ordinaryAccount = {
  id: 8,
  name: 'ordinary-account',
  platform: 'anthropic',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  priority: 1,
  concurrency: 1,
  group_ids: [],
  extra: {},
  created_at: '2026-08-28T00:00:00Z',
  updated_at: '2026-08-28T00:00:00Z'
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        DataTable: DataTableStub,
        AccountActionMenu: AccountActionMenuStub,
        EditAccountModal: EditAccountModalStub,
        AccountBulkActionsBar: false,
        RouterLink: true,
        Teleport: true
      }
    }
  })
}

describe('AccountsView supplier-managed accounts', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAccounts.mockResolvedValue({
      items: [managedAccount, ordinaryAccount],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockResolvedValue({
      notModified: false,
      etag: 'etag',
      data: {
        items: [managedAccount, ordinaryAccount],
        total: 2,
        page: 1,
        page_size: 20,
        pages: 1
      }
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getBatchPassiveUsage.mockResolvedValue({ usage: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    getAllIncludingInactive.mockResolvedValue([])
    listEdgeAccounts.mockResolvedValue({ platform: '__by_stub__', edges: [], ts: 1 })
    listSupplierSources.mockResolvedValue([
      {
        id: 7,
        supplier_name: '佳杰',
        supplier_lane: 'VSTECS',
        endpoint: 'https://example.com',
        channel_type: 1,
        base_priority: 100,
        models: [],
        notes: '',
        created_at: '2026-08-28T00:00:00Z',
        updated_at: '2026-08-28T00:00:00Z'
      }
    ])
    duplicateAccount.mockResolvedValue({ ...managedAccount, id: 99, name: 'copy' })
    setSchedulable.mockResolvedValue({ ...managedAccount, schedulable: false })
    recoverState.mockResolvedValue({ ...managedAccount, status: 'active', schedulable: true })
    resetAccountQuota.mockResolvedValue({ ...managedAccount })
    batchClearError.mockResolvedValue({ total: 1, success: 1, failed: 0, errors: [] })
  })

  it('shows the supplier-managed badge and treats managed rows like ordinary accounts', async () => {
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-test="account-row-7"]')
    const ordinaryRow = wrapper.get('[data-test="account-row-8"]')

    expect(managedRow.text()).toContain('admin.accounts.supplierManaged.badge · 佳杰/VSTECS')
    expect(managedRow.get('[data-test="schedulable"] button').attributes('disabled')).toBeUndefined()
    expect(managedRow.get('[data-testid="account-edit-btn"]').text()).toContain('common.edit')
    expect(managedRow.findAll('[data-test="actions"] button')[1].attributes('disabled')).toBeUndefined()
    expect(ordinaryRow.get('[data-test="schedulable"] button').attributes('disabled')).toBeUndefined()
  })

  it('opens the account modal for supplier-managed rows', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="account-row-7"]').get('[data-testid="account-edit-btn"]').trigger('click')
    await flushPromises()

    const modal = wrapper.get('[data-test="edit-modal"]')
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-account-id')).toBe('7')
    expect(showError).not.toHaveBeenCalled()
  })

  it('allows duplicate of supplier-managed accounts like ordinary accounts', async () => {
    const wrapper = mountView()
    await flushPromises()

    const managedRow = wrapper.get('[data-test="account-row-7"]')
    await managedRow.findAll('[data-test="actions"] button')[2].trigger('click')
    await wrapper.get('[data-test="menu-duplicate"]').trigger('click')
    await flushPromises()

    expect(duplicateAccount).toHaveBeenCalledWith(7)
    expect(showError).not.toHaveBeenCalled()
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
  })

  it('allows supplier-managed bulk runtime recovery', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="account-row-7"]').get('[data-test="select"] input').trigger('change')
    const bulkReset = wrapper.findAll('button').find(button =>
      button.text().includes('admin.accounts.bulkActions.resetStatus')
    )
    await bulkReset!.trigger('click')
    await flushPromises()

    expect(batchClearError).toHaveBeenCalled()
  })
})
